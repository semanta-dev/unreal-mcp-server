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
#include "JsonObjectConverter.h"
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
	RegisterWidget(WidgetBP, Child);
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

static FString MCPJsonResult(bool bOk, const FString& Error, TSharedPtr<FJsonValue> Value = nullptr)
{
	TSharedRef<FJsonObject> Obj = MakeShared<FJsonObject>();
	Obj->SetBoolField(TEXT("ok"), bOk);
	if (!Error.IsEmpty())
	{
		Obj->SetStringField(TEXT("error"), Error);
	}
	if (Value.IsValid())
	{
		Obj->SetField(TEXT("value"), Value);
	}
	FString Out;
	TSharedRef<TJsonWriter<>> W = TJsonWriterFactory<>::Create(&Out);
	FJsonSerializer::Serialize(Obj, W);
	return Out;
}

static FProperty* MCPDefaultProperty(UBlueprint* Blueprint, const FString& PropertyName, UObject*& OutCDO, FString& OutError)
{
	if (!Blueprint || !Blueprint->GeneratedClass)
	{
		OutError = TEXT("not a compiled Blueprint");
		return nullptr;
	}
	OutCDO = Blueprint->GeneratedClass->GetDefaultObject();
	FProperty* Prop = FindFProperty<FProperty>(Blueprint->GeneratedClass, FName(*PropertyName));
	if (!Prop)
	{
		OutError = FString::Printf(TEXT("%s has no property %s"), *Blueprint->GeneratedClass->GetName(), *PropertyName);
	}
	return Prop;
}

FString UMCPAuthoringSubsystem::GetClassDefaultJson(UBlueprint* Blueprint, const FString& PropertyName)
{
	UObject* CDO = nullptr;
	FString Error;
	FProperty* Prop = MCPDefaultProperty(Blueprint, PropertyName, CDO, Error);
	if (!Prop)
	{
		return MCPJsonResult(false, Error);
	}
	TSharedPtr<FJsonValue> Value = FJsonObjectConverter::UPropertyToJsonValue(Prop, Prop->ContainerPtrToValuePtr<void>(CDO));
	return Value.IsValid() ? MCPJsonResult(true, FString(), Value) : MCPJsonResult(false, TEXT("could not convert the property to JSON"));
}

FString UMCPAuthoringSubsystem::SetClassDefaultJson(UBlueprint* Blueprint, const FString& PropertyName, const FString& JsonValue)
{
	UObject* CDO = nullptr;
	FString Error;
	FProperty* Prop = MCPDefaultProperty(Blueprint, PropertyName, CDO, Error);
	if (!Prop)
	{
		return MCPJsonResult(false, Error);
	}
	// Parse the value by wrapping it: {"v": <JsonValue>}.
	TSharedPtr<FJsonObject> Wrapper;
	if (!FJsonSerializer::Deserialize(TJsonReaderFactory<>::Create(FString(TEXT("{\"v\":")) + JsonValue + TEXT("}")), Wrapper) || !Wrapper.IsValid())
	{
		return MCPJsonResult(false, TEXT("the value is not valid JSON"));
	}
	if (!Prop->HasAnyPropertyFlags(CPF_Edit) || Prop->HasAnyPropertyFlags(CPF_EditConst))
	{
		return MCPJsonResult(false, FString::Printf(TEXT("%s is not an editable class default"), *PropertyName));
	}
	// All or nothing: convert into a temporary value first, so a JSON that fits only
	// partly never leaves the class default half-written.
	void* Temp = FMemory::Malloc(Prop->GetSize(), Prop->GetMinAlignment());
	Prop->InitializeValue(Temp);
	const bool bOk = FJsonObjectConverter::JsonValueToUProperty(Wrapper->TryGetField(TEXT("v")), Prop, Temp, 0, 0);
	if (bOk)
	{
		CDO->Modify();
		Prop->CopyCompleteValue(Prop->ContainerPtrToValuePtr<void>(CDO), Temp);
	}
	Prop->DestroyValue(Temp);
	FMemory::Free(Temp);
	if (!bOk)
	{
		return MCPJsonResult(false, FString::Printf(TEXT("the JSON does not fit %s (%s); nothing changed"), *PropertyName, *Prop->GetCPPType()));
	}
	FBlueprintEditorUtils::MarkBlueprintAsModified(Blueprint);
	return MCPJsonResult(true, FString());
}

UWidgetTree* UMCPAuthoringSubsystem::GetWidgetTree(UWidgetBlueprint* WidgetBP)
{
	return WidgetBP ? WidgetBP->WidgetTree.Get() : nullptr;
}

UWidget* UMCPAuthoringSubsystem::GetRootWidget(UWidgetBlueprint* WidgetBP)
{
	return WidgetBP && WidgetBP->WidgetTree ? WidgetBP->WidgetTree->RootWidget.Get() : nullptr;
}

bool UMCPAuthoringSubsystem::SetRootWidget(UWidgetBlueprint* WidgetBP, UWidget* Widget)
{
	if (!WidgetBP || !WidgetBP->WidgetTree || !Widget || Widget->GetOuter() != WidgetBP->WidgetTree)
	{
		return false; // the root must be a widget of this tree
	}
	WidgetBP->WidgetTree->Modify();
	if (Widget->GetParent())
	{
		Widget->RemoveFromParent(); // a root has no parent
	}
	WidgetBP->WidgetTree->RootWidget = Widget;
	RegisterWidget(WidgetBP, Widget);
	FBlueprintEditorUtils::MarkBlueprintAsStructurallyModified(WidgetBP);
	return true;
}

bool UMCPAuthoringSubsystem::RegisterWidget(UWidgetBlueprint* WidgetBP, UWidget* Widget)
{
	if (!WidgetBP || !Widget)
	{
		return false;
	}
	if (!WidgetBP->WidgetVariableNameToGuidMap.Contains(Widget->GetFName()))
	{
		WidgetBP->OnVariableAdded(Widget->GetFName());
	}
	return true;
}

bool UMCPAuthoringSubsystem::SetWidgetIsVariable(UWidget* Widget, bool bIsVariable)
{
	if (!Widget)
	{
		return false;
	}
	Widget->Modify();
	Widget->bIsVariable = bIsVariable;
	return true;
}

bool UMCPAuthoringSubsystem::UnregisterWidget(UWidgetBlueprint* WidgetBP, FName WidgetName)
{
	if (!WidgetBP)
	{
		return false;
	}
	if (WidgetBP->WidgetVariableNameToGuidMap.Contains(WidgetName))
	{
		WidgetBP->OnVariableRemoved(WidgetName);
	}
	return true;
}
