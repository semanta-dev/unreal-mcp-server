// Copyright unreal-mcp-server. MIT.
#include "MCPControlSubsystem.h"
#include "Blueprint/UserWidget.h"
#include "Blueprint/SlateBlueprintLibrary.h"
#include "Serialization/JsonWriter.h"
#include "Serialization/JsonSerializer.h"
#include "Dom/JsonObject.h"
#include "Components/EditableTextBox.h"
#include "Components/RichTextBlock.h"
#include "Components/TextBlock.h"
#include "Blueprint/WidgetTree.h"
#include "Blueprint/WidgetBlueprintLibrary.h"

#include "Engine/Engine.h"        // GEngine, UEngine::GetWorldFromContextObject
#include "Engine/GameInstance.h"  // UGameInstance::GetSubsystem<T>
#include "Engine/World.h"
#include "GameFramework/PlayerController.h"
#include "InputKeyEventArgs.h"
#include "Kismet/GameplayStatics.h"
#include "TimerManager.h"

UMCPControlSubsystem* UMCPControlSubsystem::Get(const UObject* WorldContext)
{
	if (!WorldContext)
	{
		return nullptr;
	}
	const UWorld* World = GEngine ? GEngine->GetWorldFromContextObject(WorldContext, EGetWorldErrorMode::ReturnNull) : nullptr;
	if (!World)
	{
		return nullptr;
	}
	if (UGameInstance* GI = World->GetGameInstance())
	{
		return GI->GetSubsystem<UMCPControlSubsystem>();
	}
	return nullptr;
}

APlayerController* UMCPControlSubsystem::ResolvePC() const
{
	UWorld* World = GetWorld();
	return World ? UGameplayStatics::GetPlayerController(World, 0) : nullptr;
}

bool UMCPControlSubsystem::DispatchKey(const FKey& Key, bool bPressed)
{
	APlayerController* PC = ResolvePC();
	if (!PC || !Key.IsValid())
	{
		return false;
	}
	// Feed the player's input the same way a hardware key would, via the engine's
	// own simulated-input path (FInputKeyParams + InputKey(FInputKeyParams) are
	// deprecated in 5.6 and FInputKeyParams isn't even a complete type here). A
	// single IE_Pressed sets the axis RawValue to 1.0 and it persists across held
	// frames, so this drives legacy AXIS (WASD movement) + action + Enhanced Input.
	FInputKeyEventArgs Args = FInputKeyEventArgs::CreateSimulated(
		Key, bPressed ? IE_Pressed : IE_Released, /*AmountDepressed=*/bPressed ? 1.0f : 0.0f);
	// InputKey's bool reports whether an ACTION binding CONSUMED the event. Axis-mapped
	// keys (WASD movement) are never "consumed", so it returns false even though the axis
	// RawValue IS set and the pawn moves (validated: a DefaultPawn goes 0 -> 1200 uu/s
	// under injected W). Success here means the event was dispatched to a valid player
	// controller — not that some action binding ate it — so we ignore the consumed-bool.
	PC->InputKey(Args);
	return true;
}

bool UMCPControlSubsystem::InjectKeyByName(const FString& KeyName, bool bPressed)
{
	// Copy-init (not FKey Key(FName(*KeyName))) to avoid C++'s most-vexing-parse,
	// which would read that as a function declaration.
	const FKey Key = FKey(FName(*KeyName));
	if (!DispatchKey(Key, bPressed))
	{
		return false;
	}
	if (bPressed)
	{
		HeldKeys.Add(KeyName);
	}
	else
	{
		HeldKeys.Remove(KeyName);
	}
	return true;
}

void UMCPControlSubsystem::ScheduleRelease(const FString& KeyName, float DelaySeconds)
{
	UWorld* World = GetWorld();
	if (!World)
	{
		return;
	}
	// Cancel any pending release for this key first, so re-pressing/extending a
	// hold doesn't get cut short by the earlier timer.
	if (FTimerHandle* Existing = ReleaseTimers.Find(KeyName))
	{
		World->GetTimerManager().ClearTimer(*Existing);
	}
	FTimerHandle Handle;
	TWeakObjectPtr<UMCPControlSubsystem> WeakThis(this);
	World->GetTimerManager().SetTimer(
		Handle,
		FTimerDelegate::CreateLambda([WeakThis, KeyName]()
		{
			if (UMCPControlSubsystem* Self = WeakThis.Get())
			{
				Self->ReleaseTimers.Remove(KeyName); // one-shot fired; drop the handle
				Self->InjectKeyByName(KeyName, false);
			}
		}),
		FMath::Max(DelaySeconds, 0.01f), /*bLoop=*/false);
	ReleaseTimers.Add(KeyName, Handle);
}

bool UMCPControlSubsystem::TapKey(const FString& KeyName)
{
	// Press now; release next frame-ish so the game samples the pressed state.
	if (!InjectKeyByName(KeyName, true))
	{
		return false;
	}
	ScheduleRelease(KeyName, 0.05f);
	return true;
}

bool UMCPControlSubsystem::HoldKey(const FString& KeyName, float DurationSeconds)
{
	if (!InjectKeyByName(KeyName, true))
	{
		return false;
	}
	ScheduleRelease(KeyName, DurationSeconds);
	return true;
}

void UMCPControlSubsystem::ReleaseAll()
{
	// Copy: InjectKeyByName mutates HeldKeys as it releases.
	TArray<FString> Keys = HeldKeys.Array();
	for (const FString& K : Keys)
	{
		DispatchKey(FKey(FName(*K)), false);
	}
	HeldKeys.Reset();
	if (UWorld* World = GetWorld())
	{
		for (TPair<FString, FTimerHandle>& Pair : ReleaseTimers)
		{
			World->GetTimerManager().ClearTimer(Pair.Value);
		}
	}
	ReleaseTimers.Reset();
}

void UMCPControlSubsystem::Deinitialize()
{
	ReleaseAll();
	Super::Deinitialize();
}

UUserWidget* UMCPControlSubsystem::MountWidget(TSubclassOf<UUserWidget> WidgetClass, int32 ZOrder)
{
	APlayerController* PC = ResolvePC();
	if (!PC || !WidgetClass)
	{
		return nullptr;
	}
	UUserWidget* W = CreateWidget<UUserWidget>(PC, WidgetClass);
	if (!W)
	{
		return nullptr;
	}
	W->AddToViewport(ZOrder);
	Mounted.Add(W);
	return W;
}

int32 UMCPControlSubsystem::UnmountWidget(TSubclassOf<UUserWidget> WidgetClass)
{
	int32 N = 0;
	for (int32 i = Mounted.Num() - 1; i >= 0; --i)
	{
		UUserWidget* W = Mounted[i];
		if (!W || (WidgetClass && !W->IsA(WidgetClass)))
		{
			continue;
		}
		W->RemoveFromParent();
		Mounted.RemoveAt(i);
		++N;
	}
	return N;
}

FString UMCPControlSubsystem::DescribeLiveWidgets(TSubclassOf<UUserWidget> WidgetClass) const
{
	TArray<TSharedPtr<FJsonValue>> Out;
	UWorld* World = GetWorld();
	if (World)
	{
		TArray<UUserWidget*> Found;
		UWidgetBlueprintLibrary::GetAllWidgetsOfClass(World, Found, WidgetClass ? WidgetClass.Get() : UUserWidget::StaticClass(), false);
		for (UUserWidget* W : Found)
		{
			if (!W || !W->WidgetTree)
			{
				continue;
			}
			TSharedRef<FJsonObject> J = MakeShared<FJsonObject>();
			J->SetStringField(TEXT("widget"), W->GetName());
			J->SetStringField(TEXT("class"), W->GetClass()->GetName());
			J->SetBoolField(TEXT("in_viewport"), W->IsInViewport());
			TArray<TSharedPtr<FJsonValue>> Nodes;
			W->WidgetTree->ForEachWidget([&Nodes, W](UWidget* N)
			{
				if (!N)
				{
					return;
				}
				TSharedRef<FJsonObject> NJ = MakeShared<FJsonObject>();
				NJ->SetStringField(TEXT("name"), N->GetName());
				NJ->SetStringField(TEXT("class"), N->GetClass()->GetName());
				NJ->SetBoolField(TEXT("visible"), N->IsVisible());
				const FGeometry& G = N->GetCachedGeometry();
				FVector2D Pixel, Viewport;
				USlateBlueprintLibrary::LocalToViewport(W, G, FVector2D::ZeroVector, Pixel, Viewport);
				const FVector2D Size = G.GetAbsoluteSize();
				NJ->SetArrayField(TEXT("position"), {MakeShared<FJsonValueNumber>(Pixel.X), MakeShared<FJsonValueNumber>(Pixel.Y)});
				NJ->SetArrayField(TEXT("size"), {MakeShared<FJsonValueNumber>(Size.X), MakeShared<FJsonValueNumber>(Size.Y)});
				if (const UTextBlock* T = Cast<UTextBlock>(N))
				{
					NJ->SetStringField(TEXT("text"), T->GetText().ToString());
				}
				else if (const URichTextBlock* RT = Cast<URichTextBlock>(N))
				{
					NJ->SetStringField(TEXT("text"), RT->GetText().ToString());
				}
				else if (const UEditableTextBox* E = Cast<UEditableTextBox>(N))
				{
					NJ->SetStringField(TEXT("text"), E->GetText().ToString());
				}
				Nodes.Add(MakeShared<FJsonValueObject>(NJ));
			});
			J->SetArrayField(TEXT("nodes"), Nodes);
			Out.Add(MakeShared<FJsonValueObject>(J));
		}
	}
	FString S;
	TSharedRef<TJsonWriter<>> Wr = TJsonWriterFactory<>::Create(&S);
	FJsonSerializer::Serialize(Out, Wr);
	return S;
}
