// Copyright unreal-mcp-server. MIT.
#include "MCPCaptureSubsystem.h"

#include "Engine/SceneCapture2D.h"
#include "Components/SceneCaptureComponent2D.h"
#include "Engine/TextureRenderTarget2D.h"
#include "Kismet/KismetRenderingLibrary.h"
#include "Kismet/GameplayStatics.h"
#include "Camera/PlayerCameraManager.h"
#include "GameFramework/PlayerController.h"
#include "Engine/World.h"
#include "TimerManager.h"
#include "EngineUtils.h" // TActorIterator
#include "HAL/FileManager.h"
#include "Misc/FileHelper.h"
#include "Misc/Paths.h"
#include "Dom/JsonObject.h"
#include "Serialization/JsonSerializer.h"
#include "Framework/Application/SlateApplication.h"
#include "Widgets/SViewport.h" // complete SViewport for the GetGameViewportWidget() -> TSharedPtr<SWidget> upcast
#include "Engine/GameViewportClient.h"
#include "Engine/Engine.h"        // GEngine, GetWorldFromContextObject
#include "Engine/GameInstance.h"  // GetSubsystem<T>
#include "IImageWrapper.h"
#include "IImageWrapperModule.h"
#include "Modules/ModuleManager.h"

// Encode a BGRA FColor array as a real PNG via the ImageWrapper module. UE's
// FImageUtils::CompressImageArray emits JPEG in 5.7 (which our .png-named files
// and the read_capture montage's png.Decode reject), so encode PNG explicitly.
static bool MCPSavePNG(const FString& Path, const TArray<FColor>& Pixels, int32 W, int32 H)
{
	if (Pixels.Num() < W * H || W <= 0 || H <= 0)
	{
		return false;
	}
	IImageWrapperModule& Module = FModuleManager::LoadModuleChecked<IImageWrapperModule>(TEXT("ImageWrapper"));
	TSharedPtr<IImageWrapper> Wrapper = Module.CreateImageWrapper(EImageFormat::PNG);
	if (!Wrapper.IsValid())
	{
		return false;
	}
	if (!Wrapper->SetRaw(Pixels.GetData(), (int64)Pixels.Num() * sizeof(FColor), W, H, ERGBFormat::BGRA, 8))
	{
		return false;
	}
	const TArray64<uint8>& Png = Wrapper->GetCompressed(100);
	return FFileHelper::SaveArrayToFile(Png, *Path);
}

UMCPCaptureSubsystem* UMCPCaptureSubsystem::Get(const UObject* WorldContext)
{
	if (!GEngine)
	{
		return nullptr;
	}
	if (const UWorld* World = GEngine->GetWorldFromContextObject(WorldContext, EGetWorldErrorMode::ReturnNull))
	{
		if (UGameInstance* GI = World->GetGameInstance())
		{
			return GI->GetSubsystem<UMCPCaptureSubsystem>();
		}
	}
	return nullptr;
}

FString UMCPCaptureSubsystem::StartCapture(const FString& InSession, const FString& InOutDir, const FString& InPrefix,
	int32 InWidth, int32 InHeight, float InInterval, int32 InMaxFrames, float InMaxSeconds,
	EMCPCaptureCamera InCamera, FVector InFixedLoc, FRotator InFixedRot, float InFixedFOV, const FString& InActorLabel,
	bool bInIncludeUI)
{
	if (bRunning)
	{
		return FString();
	}
	UWorld* World = GetWorld();
	if (!World)
	{
		return FString();
	}

	Session = InSession;
	OutDir = InOutDir;
	FilePrefix = InPrefix;
	Width = FMath::Max(InWidth, 16);
	Height = FMath::Max(InHeight, 16);
	IntervalSeconds = FMath::Max(InInterval, 0.05f);
	MaxFrames = InMaxFrames > 0 ? InMaxFrames : 240;
	MaxSeconds = InMaxSeconds > 0.f ? InMaxSeconds : 60.f;
	Camera = InCamera;
	FixedLocation = InFixedLoc;
	FixedRotation = InFixedRot;
	FixedFOV = InFixedFOV > 0.f ? InFixedFOV : 90.f;
	ActorLabel = InActorLabel;
	bIncludeUI = bInIncludeUI;
	Frames.Reset();
	StopReason.Reset();

	IFileManager::Get().MakeDirectory(*OutDir, /*Tree=*/true);

	// UI mode grabs the composited on-screen viewport via Slate — no SceneCapture
	// actor / render target needed. 3D mode spawns a SceneCapture2D into the game
	// world (the whole point — Python's EditorActorSubsystem can only reach the
	// editor world), which renders backgrounded but does NOT include the HUD.
	if (!bIncludeUI)
	{
		RenderTarget = UKismetRenderingLibrary::CreateRenderTarget2D(World, Width, Height, RTF_RGBA8, FLinearColor::Black, false);
		if (!RenderTarget)
		{
			return FString();
		}
		FActorSpawnParameters SpawnParams;
		SpawnParams.ObjectFlags |= RF_Transient;
		CaptureActor = World->SpawnActor<ASceneCapture2D>(FVector::ZeroVector, FRotator::ZeroRotator, SpawnParams);
		if (!CaptureActor)
		{
			RenderTarget = nullptr;
			return FString();
		}
		CaptureComp = CaptureActor->GetCaptureComponent2D();
		if (!CaptureComp)
		{
			Teardown();
			return FString();
		}
		CaptureComp->TextureTarget = RenderTarget;
		CaptureComp->CaptureSource = ESceneCaptureSource::SCS_FinalColorLDR;
		CaptureComp->bCaptureEveryFrame = false;
		CaptureComp->bCaptureOnMovement = false;
		CaptureComp->bAlwaysPersistRenderingState = true;
	}

	StartWorldTime = World->GetTimeSeconds();
	bRunning = true;
	// Fire immediately (first delay 0), then every interval.
	World->GetTimerManager().SetTimer(CaptureTimer, this, &UMCPCaptureSubsystem::OnCaptureTick, IntervalSeconds, true, 0.f);
	return Session;
}

void UMCPCaptureSubsystem::OnCaptureTick()
{
	if (!bRunning)
	{
		return;
	}
	CaptureFrame();
	UWorld* World = GetWorld();
	const float Elapsed = World ? (World->GetTimeSeconds() - StartWorldTime) : 0.f;
	if (Frames.Num() >= MaxFrames)
	{
		SelfStop(TEXT("max_frames"));
	}
	else if (Elapsed >= MaxSeconds)
	{
		SelfStop(TEXT("max_seconds"));
	}
	else if (Frames.Num() % 20 == 0)
	{
		FlushManifest();
	}
}

void UMCPCaptureSubsystem::CaptureFrame()
{
	UWorld* World = GetWorld();
	if (!World)
	{
		return;
	}
	const int32 Idx = Frames.Num();
	const FString Rel = FString::Printf(TEXT("%sf%05d.png"), *FilePrefix, Idx);
	bool bOk = false;
	if (bIncludeUI)
	{
		bOk = CaptureViewportUI(FPaths::Combine(OutDir, Rel));
	}
	else if (CaptureComp && RenderTarget)
	{
		PositionCapture();
		CaptureComp->CaptureScene();
		UKismetRenderingLibrary::ExportRenderTarget(World, RenderTarget, OutDir, Rel);
		bOk = true;
	}
	if (!bOk)
	{
		// A failed frame keeps its index (the next attempt reuses the filename) so
		// a transient miss (e.g. no rendered window yet) doesn't leave a gap.
		return;
	}
	FMCPFrame F;
	F.Index = Idx;
	F.File = Rel;
	F.TWorld = World->GetTimeSeconds();
	F.TWall = F.TWorld - StartWorldTime;
	Frames.Add(F);
}

bool UMCPCaptureSubsystem::CaptureViewportUI(const FString& AbsPath)
{
	if (!FSlateApplication::IsInitialized() || !GEngine || !GEngine->GameViewport)
	{
		return false;
	}
	TSharedPtr<SWidget> ViewportWidget = GEngine->GameViewport->GetGameViewportWidget();
	if (!ViewportWidget.IsValid())
	{
		return false;
	}
	const FVector2D LocalSize = ViewportWidget->GetTickSpaceGeometry().GetLocalSize();
	const FIntRect Rect(0, 0, FMath::Max(1, (int32)LocalSize.X), FMath::Max(1, (int32)LocalSize.Y));
	TArray<FColor> Pixels;
	FIntVector OutSize(0, 0, 0);
	// Reads the window backbuffer for the viewport widget = the composited scene
	// PLUS the Slate/UMG HUD (what HighResShot grabs, but without needing its
	// foreground frame-flush) — reliable while the window is actually rendering.
	if (!FSlateApplication::Get().TakeScreenshot(ViewportWidget.ToSharedRef(), Rect, Pixels, OutSize))
	{
		return false;
	}
	if (Pixels.Num() == 0 || OutSize.X <= 0 || OutSize.Y <= 0)
	{
		return false;
	}
	for (FColor& C : Pixels)
	{
		C.A = 255; // opaque
	}
	return MCPSavePNG(AbsPath, Pixels, OutSize.X, OutSize.Y);
}

bool UMCPCaptureSubsystem::PositionCapture()
{
	UWorld* World = GetWorld();
	if (!CaptureActor || !World)
	{
		return false;
	}
	switch (Camera)
	{
	case EMCPCaptureCamera::Fixed:
		CaptureActor->SetActorLocationAndRotation(FixedLocation, FixedRotation);
		if (CaptureComp)
		{
			CaptureComp->FOVAngle = FixedFOV;
		}
		return true;

	case EMCPCaptureCamera::Actor:
		for (TActorIterator<AActor> It(World); It; ++It)
		{
			AActor* A = *It;
			if (!A)
			{
				continue;
			}
			FString Name;
#if WITH_EDITOR
			Name = A->GetActorLabel();
#else
			Name = A->GetName();
#endif
			if (Name == ActorLabel)
			{
				CaptureActor->SetActorLocationAndRotation(A->GetActorLocation(), A->GetActorRotation());
				return true;
			}
		}
		return false;

	case EMCPCaptureCamera::Player:
	default:
	{
		APlayerController* PC = UGameplayStatics::GetPlayerController(World, 0);
		if (PC && PC->PlayerCameraManager)
		{
			CaptureActor->SetActorLocationAndRotation(PC->PlayerCameraManager->GetCameraLocation(),
				PC->PlayerCameraManager->GetCameraRotation());
			if (CaptureComp)
			{
				CaptureComp->FOVAngle = PC->PlayerCameraManager->GetFOVAngle();
			}
			return true;
		}
		return false;
	}
	}
}

void UMCPCaptureSubsystem::SelfStop(const FString& Reason)
{
	if (StopReason.IsEmpty())
	{
		StopReason = Reason;
	}
	bRunning = false;
	if (UWorld* World = GetWorld())
	{
		World->GetTimerManager().ClearTimer(CaptureTimer);
	}
	FlushManifest();
	Teardown();
}

FString UMCPCaptureSubsystem::StopCapture()
{
	if (bRunning)
	{
		SelfStop(TEXT("requested"));
	}
	else
	{
		FlushManifest();
	}
	return BuildManifestJson();
}

void UMCPCaptureSubsystem::Teardown()
{
	if (CaptureActor)
	{
		CaptureActor->Destroy();
		CaptureActor = nullptr;
	}
	CaptureComp = nullptr;
	RenderTarget = nullptr;
}

void UMCPCaptureSubsystem::Deinitialize()
{
	if (bRunning)
	{
		SelfStop(TEXT("deinitialize"));
	}
	Super::Deinitialize();
}

FString UMCPCaptureSubsystem::BuildManifestJson() const
{
	TSharedRef<FJsonObject> Root = MakeShared<FJsonObject>();
	Root->SetStringField(TEXT("dir"), OutDir);
	Root->SetStringField(TEXT("session"), Session);
	Root->SetStringField(TEXT("backend"), TEXT("plugin"));
	Root->SetNumberField(TEXT("frame_count"), Frames.Num());
	Root->SetNumberField(TEXT("cell_width"), Width);
	Root->SetNumberField(TEXT("cell_height"), Height);
	Root->SetStringField(TEXT("stop_reason"), StopReason.IsEmpty() ? TEXT("requested") : StopReason);

	TArray<TSharedPtr<FJsonValue>> Arr;
	for (const FMCPFrame& F : Frames)
	{
		TSharedPtr<FJsonObject> J = MakeShared<FJsonObject>();
		J->SetNumberField(TEXT("index"), F.Index);
		J->SetStringField(TEXT("file"), F.File);
		J->SetNumberField(TEXT("t_wall"), F.TWall);
		J->SetNumberField(TEXT("t_world"), F.TWorld);
		J->SetObjectField(TEXT("state"), MakeShared<FJsonObject>()); // no per-frame reflected state from C++
		Arr.Add(MakeShared<FJsonValueObject>(J));
	}
	Root->SetArrayField(TEXT("frames"), Arr);

	FString Out;
	TSharedRef<TJsonWriter<>> Writer = TJsonWriterFactory<>::Create(&Out);
	FJsonSerializer::Serialize(Root, Writer);
	return Out;
}

void UMCPCaptureSubsystem::FlushManifest() const
{
	const FString Path = FPaths::Combine(OutDir, TEXT("manifest.json"));
	FFileHelper::SaveStringToFile(BuildManifestJson(), *Path);
}

FString UMCPCaptureSubsystem::PollCapture() const
{
	TSharedRef<FJsonObject> Root = MakeShared<FJsonObject>();
	Root->SetBoolField(TEXT("running"), bRunning);
	Root->SetNumberField(TEXT("frames_captured"), Frames.Num());
	Root->SetNumberField(TEXT("last_t_world"), Frames.Num() ? Frames.Last().TWorld : 0.f);
	Root->SetStringField(TEXT("dir"), OutDir);

	FString Out;
	TSharedRef<TJsonWriter<>> Writer = TJsonWriterFactory<>::Create(&Out);
	FJsonSerializer::Serialize(Root, Writer);
	return Out;
}
