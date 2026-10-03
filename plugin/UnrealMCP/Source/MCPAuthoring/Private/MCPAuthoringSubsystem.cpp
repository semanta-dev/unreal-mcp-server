// Copyright unreal-mcp-server. MIT.
#include "MCPAuthoringSubsystem.h"

#include "WidgetBlueprint.h"
#include "Blueprint/WidgetTree.h"
#include "Components/Widget.h"
#include "Components/PanelWidget.h"
#include "Blueprint/UserWidget.h"
#include "Kismet2/KismetEditorUtilities.h"
#include "Kismet2/BlueprintEditorUtils.h"
#include "Engine/Blueprint.h"
#include "UObject/UObjectIterator.h"
#include "Kismet2/CompilerResultsLog.h"
#include "UObject/UnrealType.h"
#include "Serialization/JsonSerializer.h"
#include "Dom/JsonObject.h"
// CaptureWidget — offscreen widget render (FWidgetRenderer)
#include "Slate/WidgetRenderer.h"
#include "Engine/TextureRenderTarget2D.h"
#include "TextureResource.h"
#include "Editor.h"
#include "IImageWrapper.h"
#include "IImageWrapperModule.h"
#include "Modules/ModuleManager.h"
#include "Misc/FileHelper.h"
#include "RenderingThread.h"

static FString MCPJsonToString(const TSharedRef<FJsonObject>& Obj)
{
	FString Out;
	TSharedRef<TJsonWriter<>> Writer = TJsonWriterFactory<>::Create(&Out);
	FJsonSerializer::Serialize(Obj, Writer);
	return Out;
}

bool UMCPAuthoringSubsystem::AddChildWidget(UWidgetBlueprint* WidgetBP, FName ParentName, UClass* WidgetClass, FName NewName, bool bIsVariable)
{
	if (!WidgetBP || !WidgetBP->WidgetTree || !WidgetClass)
	{
		return false;
	}
	if (!WidgetClass->IsChildOf(UWidget::StaticClass()))
	{
		return false;
	}
	UWidgetTree* Tree = WidgetBP->WidgetTree;
	Tree->Modify();

	// The design-time embed path (identical to a palette drag). For a UserWidget child
	// this runs the proper initialization a Python new_object would skip.
	UWidget* Child = Tree->ConstructWidget<UWidget>(WidgetClass, NewName);
	if (!Child)
	{
		return false;
	}

	UPanelWidget* Parent = Cast<UPanelWidget>(Tree->FindWidget(ParentName));
	if (!Parent)
	{
		return false;
	}
	Parent->Modify();
	if (!Parent->AddChild(Child))
	{
		return false;
	}
	Child->bIsVariable = bIsVariable;
	return true;
}

FString UMCPAuthoringSubsystem::CompileWidget(UWidgetBlueprint* WidgetBP)
{
	TSharedRef<FJsonObject> Root = MakeShared<FJsonObject>();
	if (!WidgetBP)
	{
		Root->SetBoolField(TEXT("compiled"), false);
		Root->SetStringField(TEXT("error"), TEXT("null WidgetBlueprint"));
		return MCPJsonToString(Root);
	}

	FCompilerResultsLog Log;
	Log.SetSourcePath(WidgetBP->GetPathName());
	FKismetEditorUtilities::CompileBlueprint(WidgetBP, EBlueprintCompileOptions::None, &Log);

	TArray<TSharedPtr<FJsonValue>> Messages;
	for (const TSharedRef<FTokenizedMessage>& Msg : Log.Messages)
	{
		TSharedRef<FJsonObject> M = MakeShared<FJsonObject>();
		const EMessageSeverity::Type Sev = Msg->GetSeverity();
		M->SetStringField(TEXT("severity"),
			Sev == EMessageSeverity::Error ? TEXT("error")
			: Sev == EMessageSeverity::Warning ? TEXT("warning")
			: TEXT("info"));
		M->SetStringField(TEXT("text"), Msg->ToText().ToString());
		Messages.Add(MakeShared<FJsonValueObject>(M));
	}

	Root->SetBoolField(TEXT("compiled"), Log.NumErrors == 0);
	Root->SetNumberField(TEXT("num_errors"), Log.NumErrors);
	Root->SetNumberField(TEXT("num_warnings"), Log.NumWarnings);
	Root->SetArrayField(TEXT("messages"), Messages);
	return MCPJsonToString(Root);
}

FString UMCPAuthoringSubsystem::DescribeBindWidgets(UClass* WidgetClass)
{
	TSharedRef<FJsonObject> Root = MakeShared<FJsonObject>();
	TArray<TSharedPtr<FJsonValue>> Binds;
	if (WidgetClass)
	{
		static const FName NAME_BindWidget(TEXT("BindWidget"));
		static const FName NAME_BindWidgetOptional(TEXT("BindWidgetOptional"));
		for (TFieldIterator<FObjectPropertyBase> It(WidgetClass); It; ++It)
		{
			FObjectPropertyBase* Prop = *It;
			const bool bReq = Prop->HasMetaData(NAME_BindWidget);
			const bool bOpt = Prop->HasMetaData(NAME_BindWidgetOptional);
			if (!bReq && !bOpt)
			{
				continue;
			}
			TSharedRef<FJsonObject> B = MakeShared<FJsonObject>();
			B->SetStringField(TEXT("name"), Prop->GetName());
			B->SetStringField(TEXT("type"), Prop->PropertyClass ? Prop->PropertyClass->GetName() : TEXT("UWidget"));
			B->SetBoolField(TEXT("optional"), bOpt);
			Binds.Add(MakeShared<FJsonValueObject>(B));
		}
	}
	Root->SetArrayField(TEXT("bindwidgets"), Binds);
	return MCPJsonToString(Root);
}

// PNG-encode a BGRA FColor array via the ImageWrapper module (FImageUtils emits JPEG in 5.7).
static bool MCPAuthSavePNG(const FString& Path, const TArray<FColor>& Pixels, int32 W, int32 H)
{
	if (Pixels.Num() < W * H || W <= 0 || H <= 0)
	{
		return false;
	}
	IImageWrapperModule& Module = FModuleManager::LoadModuleChecked<IImageWrapperModule>(TEXT("ImageWrapper"));
	TSharedPtr<IImageWrapper> Wrapper = Module.CreateImageWrapper(EImageFormat::PNG);
	if (!Wrapper.IsValid() || !Wrapper->SetRaw(Pixels.GetData(), (int64)Pixels.Num() * sizeof(FColor), W, H, ERGBFormat::BGRA, 8))
	{
		return false;
	}
	const TArray64<uint8>& Png = Wrapper->GetCompressed(100);
	return FFileHelper::SaveArrayToFile(Png, *Path);
}

FString UMCPAuthoringSubsystem::CaptureWidget(const FString& WidgetClassPath, int32 Width, int32 Height, const FString& OutPath)
{
	if (Width <= 0 || Height <= 0)
	{
		return FString();
	}
	UClass* Cls = LoadClass<UUserWidget>(nullptr, *WidgetClassPath);
	if (!Cls)
	{
		return FString();
	}
	UWorld* World = GEditor ? GEditor->GetEditorWorldContext().World() : nullptr;
	if (!World)
	{
		return FString();
	}
	UUserWidget* Widget = CreateWidget<UUserWidget>(World, Cls);
	if (!Widget)
	{
		return FString();
	}
	TSharedRef<SWidget> Slate = Widget->TakeWidget();

	UTextureRenderTarget2D* RT = NewObject<UTextureRenderTarget2D>(this);
	RT->RenderTargetFormat = ETextureRenderTargetFormat::RTF_RGBA8;
	RT->ClearColor = FLinearColor(0.f, 0.f, 0.f, 0.f);
	RT->InitAutoFormat(Width, Height);
	RT->UpdateResourceImmediate(true);

	FWidgetRenderer* Renderer = new FWidgetRenderer(/*bUseGammaCorrection=*/true);
	// A few draws warm up fonts/layout so text isn't missing on the first frame.
	for (int32 i = 0; i < 3; ++i)
	{
		Renderer->DrawWidget(RT, Slate, FVector2D(Width, Height), 0.f, false);
		FlushRenderingCommands();
	}

	bool bOk = false;
	if (FTextureRenderTargetResource* Res = RT->GameThread_GetRenderTargetResource())
	{
		TArray<FColor> Pixels;
		FReadSurfaceDataFlags Flags(RCM_UNorm, CubeFace_MAX);
		if (Res->ReadPixels(Pixels, Flags))
		{
			bOk = MCPAuthSavePNG(OutPath, Pixels, Width, Height);
		}
	}
	BeginCleanup(Renderer);
	return bOk ? OutPath : FString();
}

TArray<FString> UMCPAuthoringSubsystem::PrepareBlueprintsForPIE(bool bAcknowledgeErrors, int32& CompiledCount)
{
	CompiledCount = 0;
	// What PIE would compile first (dirty, not data-only, status known). Compiling them
	// here also avoids the "compile before playing?" prompt when auto-recompile is off.
	TArray<UBlueprint*> ToCompile;
	for (TObjectIterator<UBlueprint> It; It; ++It)
	{
		UBlueprint* Blueprint = *It;
		if (IsValid(Blueprint) && !Blueprint->IsUpToDate() && Blueprint->IsPossiblyDirty()
			&& Blueprint->Status != BS_Unknown && !FBlueprintEditorUtils::IsDataOnlyBlueprint(Blueprint))
		{
			ToCompile.Add(Blueprint);
		}
	}
	for (UBlueprint* Blueprint : ToCompile)
	{
		if (IsValid(Blueprint))
		{
			FKismetEditorUtilities::CompileBlueprint(Blueprint, EBlueprintCompileOptions::SkipGarbageCollection);
			++CompiledCount;
		}
	}
	if (CompiledCount > 0)
	{
		// As ResolveDirtyBlueprints does once after its loop: drop the REINST leftovers
		// before PIE duplicates the world.
		CollectGarbage(GARBAGE_COLLECTION_KEEPFLAGS);
	}
	// What PIE's dialog would then list.
	TArray<FString> Errored;
	for (TObjectIterator<UBlueprint> It; It; ++It)
	{
		UBlueprint* Blueprint = *It;
		if (IsValid(Blueprint) && Blueprint->Status == BS_Error && Blueprint->bDisplayCompilePIEWarning)
		{
			Errored.Add(Blueprint->GetPathName());
			if (bAcknowledgeErrors)
			{
				Blueprint->bDisplayCompilePIEWarning = false;
			}
		}
	}
	return Errored;
}
