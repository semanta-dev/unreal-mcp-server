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
#include "Curves/CurveFloat.h"
#include "Engine/SimpleConstructionScript.h"
#include "Engine/SCS_Node.h"
#include "K2Node_Event.h"
#include "K2Node_CustomEvent.h"
#include "EdGraphSchema_K2.h"
#include "GameFramework/Actor.h"
#include "Kismet2/Kismet2NameValidators.h"

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

// --- API 6: curve keys, Blueprint describe ---------------------------------------------

static const TCHAR* MCPInterpName(ERichCurveInterpMode Mode)
{
	switch (Mode)
	{
	case RCIM_Constant: return TEXT("constant");
	case RCIM_Cubic: return TEXT("cubic");
	case RCIM_None: return TEXT("none");
	default: return TEXT("linear");
	}
}

static const TCHAR* MCPTangentModeName(ERichCurveTangentMode Mode)
{
	switch (Mode)
	{
	case RCTM_User: return TEXT("user");
	case RCTM_Break: return TEXT("break");
	case RCTM_None: return TEXT("none");
	default: return TEXT("auto");
	}
}

FString UMCPAuthoringSubsystem::GetCurveKeysJson(UCurveFloat* Curve)
{
	TArray<TSharedPtr<FJsonValue>> Keys;
	if (Curve)
	{
		for (auto It = Curve->FloatCurve.GetKeyIterator(); It; ++It)
		{
			const FRichCurveKey& K = *It;
			TSharedRef<FJsonObject> J = MakeShared<FJsonObject>();
			J->SetNumberField(TEXT("time"), K.Time);
			J->SetNumberField(TEXT("value"), K.Value);
			J->SetStringField(TEXT("interp"), MCPInterpName(K.InterpMode));
			J->SetNumberField(TEXT("arrive_tangent"), K.ArriveTangent);
			J->SetNumberField(TEXT("leave_tangent"), K.LeaveTangent);
			J->SetStringField(TEXT("tangent_mode"), MCPTangentModeName(K.TangentMode));
			Keys.Add(MakeShared<FJsonValueObject>(J));
		}
	}
	FString Out;
	TSharedRef<TJsonWriter<>> W = TJsonWriterFactory<>::Create(&Out);
	FJsonSerializer::Serialize(Keys, W);
	return Out;
}

FString UMCPAuthoringSubsystem::SetCurveKeysJson(UCurveFloat* Curve, const FString& KeysJson)
{
	if (!Curve)
	{
		return TEXT("not a float curve");
	}
	TArray<TSharedPtr<FJsonValue>> In;
	const TSharedRef<TJsonReader<>> R = TJsonReaderFactory<>::Create(KeysJson);
	if (!FJsonSerializer::Deserialize(R, In))
	{
		return TEXT("keys must be a JSON array of {time, value, interp?, arrive_tangent?, leave_tangent?}");
	}
	struct FKeyIn
	{
		float Time = 0.f;
		float Value = 0.f;
		ERichCurveInterpMode Mode = RCIM_Linear;
		TOptional<float> Arrive, Leave;
		bool bBreak = false;
	};
	TArray<FKeyIn> Parsed;
	TSet<float> Times;
	for (int32 i = 0; i < In.Num(); ++i)
	{
		const TSharedPtr<FJsonObject>* O = nullptr;
		if (!In[i].IsValid() || !In[i]->TryGetObject(O))
		{
			return FString::Printf(TEXT("key %d: must be an object {time, value, ...}"), i);
		}
		// Numbers only (TryGetNumberField would take true as 1), and finite ones.
		auto Number = [&](const TCHAR* Name, double& Out) -> bool
		{
			const TSharedPtr<FJsonValue> F = (*O)->TryGetField(Name);
			if (!F.IsValid() || F->Type != EJson::Number)
			{
				return false;
			}
			Out = F->AsNumber();
			return FMath::IsFinite(Out) && FMath::Abs(Out) <= (double)TNumericLimits<float>::Max();
		};
		double T = 0.0, V = 0.0;
		if (!Number(TEXT("time"), T) || !Number(TEXT("value"), V))
		{
			return FString::Printf(TEXT("key %d: needs finite numeric time and value"), i);
		}
		for (const auto& Field : (*O)->Values)
		{
			if (Field.Key != TEXT("time") && Field.Key != TEXT("value") && Field.Key != TEXT("interp")
				&& Field.Key != TEXT("arrive_tangent") && Field.Key != TEXT("leave_tangent") && Field.Key != TEXT("tangent_mode"))
			{
				return FString::Printf(TEXT("key %d: unknown field %s"), i, *Field.Key);
			}
		}
		FKeyIn K;
		K.Time = (float)T;
		K.Value = (float)V;
		FString Interp;
		if ((*O)->TryGetStringField(TEXT("interp"), Interp))
		{
			if (Interp == TEXT("linear")) { K.Mode = RCIM_Linear; }
			else if (Interp == TEXT("constant")) { K.Mode = RCIM_Constant; }
			else if (Interp == TEXT("cubic")) { K.Mode = RCIM_Cubic; }
			else if (Interp == TEXT("none")) { K.Mode = RCIM_None; }
			else { return FString::Printf(TEXT("key %d: interp must be linear, constant, cubic or none (got %s)"), i, *Interp); }
		}
		// Tangents are kept only for a key that says they are its own (tangent_mode user or
		// break, as a read reports them): an auto key read back and written is still auto.
		FString TangentMode;
		(*O)->TryGetStringField(TEXT("tangent_mode"), TangentMode);
		if (!TangentMode.IsEmpty() && TangentMode != TEXT("auto") && TangentMode != TEXT("user") && TangentMode != TEXT("break") && TangentMode != TEXT("none"))
		{
			return FString::Printf(TEXT("key %d: tangent_mode must be auto, user, break or none (got %s)"), i, *TangentMode);
		}
		const bool bOwnTangents = TangentMode == TEXT("user") || TangentMode == TEXT("break")
			|| (TangentMode.IsEmpty() && ((*O)->HasField(TEXT("arrive_tangent")) || (*O)->HasField(TEXT("leave_tangent"))));
		double Tan = 0.0;
		if (bOwnTangents && Number(TEXT("arrive_tangent"), Tan)) { K.Arrive = (float)Tan; }
		if (bOwnTangents && Number(TEXT("leave_tangent"), Tan)) { K.Leave = (float)Tan; }
		K.bBreak = TangentMode == TEXT("break");
		if (Times.Contains(K.Time))
		{
			return FString::Printf(TEXT("key %d: two keys at time %g"), i, T);
		}
		Times.Add(K.Time);
		Parsed.Add(K);
	}
	if (Parsed.Num() == 0)
	{
		return TEXT("a curve needs at least one key");
	}
	// Validated: now change it, as one edit the caller's transaction records.
	Curve->Modify();
	FRichCurve& RC = Curve->FloatCurve;
	RC.Reset();
	for (const FKeyIn& K : Parsed)
	{
		const FKeyHandle H = RC.AddKey(K.Time, K.Value);
		RC.SetKeyInterpMode(H, K.Mode);
		if (K.Arrive.IsSet() || K.Leave.IsSet())
		{
			FRichCurveKey& Key = RC.GetKey(H);
			Key.TangentMode = K.bBreak ? RCTM_Break : RCTM_User;
			Key.ArriveTangent = K.Arrive.Get(Key.ArriveTangent);
			Key.LeaveTangent = K.Leave.Get(Key.LeaveTangent);
		}
	}
	RC.AutoSetTangents();
	Curve->OnCurveChanged(Curve->GetCurves()); // open curve editors refresh
	Curve->MarkPackageDirty();
	return FString();
}

static const TCHAR* MCPBlueprintStatus(EBlueprintStatus Status)
{
	switch (Status)
	{
	case BS_Dirty: return TEXT("dirty");
	case BS_Error: return TEXT("error");
	case BS_UpToDate: return TEXT("up_to_date");
	case BS_UpToDateWithWarnings: return TEXT("up_to_date_with_warnings");
	case BS_BeingCreated: return TEXT("being_created");
	default: return TEXT("unknown");
	}
}

FString UMCPAuthoringSubsystem::DescribeBlueprintJson(UBlueprint* Blueprint, bool bCompile)
{
	TSharedRef<FJsonObject> Root = MakeShared<FJsonObject>();
	if (!Blueprint)
	{
		Root->SetStringField(TEXT("error"), TEXT("not a Blueprint"));
		return MCPJsonToString(Root);
	}
	if (bCompile)
	{
		if (GEditor && GEditor->PlayWorld)
		{
			Root->SetStringField(TEXT("error"), TEXT("compiling a Blueprint while PIE runs changes the running game: stop PIE first"));
			return MCPJsonToString(Root);
		}
		FCompilerResultsLog Log;
		Log.SetSourcePath(Blueprint->GetPathName());
		FKismetEditorUtilities::CompileBlueprint(Blueprint, EBlueprintCompileOptions::SkipSave, &Log);
		TArray<TSharedPtr<FJsonValue>> Messages;
		for (const TSharedRef<FTokenizedMessage>& Msg : Log.Messages)
		{
			TSharedRef<FJsonObject> M = MakeShared<FJsonObject>();
			const EMessageSeverity::Type Sev = Msg->GetSeverity();
			M->SetStringField(TEXT("severity"), Sev == EMessageSeverity::Error ? TEXT("error") : Sev == EMessageSeverity::Warning ? TEXT("warning") : TEXT("info"));
			M->SetStringField(TEXT("text"), Msg->ToText().ToString());
			Messages.Add(MakeShared<FJsonValueObject>(M));
		}
		TSharedRef<FJsonObject> Compile = MakeShared<FJsonObject>();
		Compile->SetNumberField(TEXT("num_errors"), Log.NumErrors);
		Compile->SetNumberField(TEXT("num_warnings"), Log.NumWarnings);
		Compile->SetArrayField(TEXT("messages"), Messages);
		Root->SetObjectField(TEXT("compile"), Compile);
	}
	Root->SetStringField(TEXT("blueprint"), Blueprint->GetPathName());
	Root->SetStringField(TEXT("parent"), Blueprint->ParentClass ? Blueprint->ParentClass->GetPathName() : FString());
	Root->SetStringField(TEXT("generated_class"), Blueprint->GeneratedClass ? Blueprint->GeneratedClass->GetPathName() : FString());
	Root->SetStringField(TEXT("status"), MCPBlueprintStatus(Blueprint->Status));

	TArray<TSharedPtr<FJsonValue>> Components;
	if (Blueprint->SimpleConstructionScript)
	{
		for (USCS_Node* Node : Blueprint->SimpleConstructionScript->GetAllNodes())
		{
			if (!Node)
			{
				continue;
			}
			TSharedRef<FJsonObject> J = MakeShared<FJsonObject>();
			J->SetStringField(TEXT("name"), Node->GetVariableName().ToString());
			J->SetStringField(TEXT("class"), Node->ComponentClass ? Node->ComponentClass->GetName() : FString());
			J->SetStringField(TEXT("parent"), Node->ParentComponentOrVariableName.ToString());
			J->SetStringField(TEXT("source"), TEXT("blueprint"));
			Components.Add(MakeShared<FJsonValueObject>(J));
		}
	}
	if (Blueprint->GeneratedClass && Blueprint->GeneratedClass->IsChildOf(AActor::StaticClass()))
	{
		// Components the parent class creates (C++ CreateDefaultSubobject), seen on the CDO.
		const AActor* CDO = Cast<AActor>(Blueprint->GeneratedClass->GetDefaultObject());
		TInlineComponentArray<UActorComponent*> Native;
		if (CDO)
		{
			CDO->GetComponents(Native);
		}
		for (const UActorComponent* Comp : Native)
		{
			if (!Comp || Comp->CreationMethod == EComponentCreationMethod::SimpleConstructionScript)
			{
				continue;
			}
			TSharedRef<FJsonObject> J = MakeShared<FJsonObject>();
			J->SetStringField(TEXT("name"), Comp->GetName());
			J->SetStringField(TEXT("class"), Comp->GetClass()->GetName());
			J->SetStringField(TEXT("source"), TEXT("native"));
			Components.Add(MakeShared<FJsonValueObject>(J));
		}
	}
	Root->SetArrayField(TEXT("components"), Components);

	TArray<TSharedPtr<FJsonValue>> Variables;
	const UObject* ClassDefaults = Blueprint->GeneratedClass ? Blueprint->GeneratedClass->GetDefaultObject(false) : nullptr;
	for (const FBPVariableDescription& V : Blueprint->NewVariables)
	{
		TSharedRef<FJsonObject> J = MakeShared<FJsonObject>();
		J->SetStringField(TEXT("name"), V.VarName.ToString());
		J->SetStringField(TEXT("type"), UEdGraphSchema_K2::TypeToText(V.VarType).ToString());
		J->SetStringField(TEXT("category"), V.Category.ToString());
		// The class default the game gets (what the Class Defaults panel shows), not the
		// variable's declared default string.
		FString Default = V.DefaultValue;
		if (const FProperty* P = ClassDefaults ? FindFProperty<FProperty>(Blueprint->GeneratedClass, V.VarName) : nullptr)
		{
			Default.Reset();
			P->ExportTextItem_Direct(Default, P->ContainerPtrToValuePtr<void>(ClassDefaults), nullptr, nullptr, PPF_None);
		}
		J->SetStringField(TEXT("default"), Default);
		J->SetBoolField(TEXT("instance_editable"), (V.PropertyFlags & CPF_DisableEditOnInstance) == 0 && (V.PropertyFlags & CPF_Edit) != 0);
		J->SetBoolField(TEXT("expose_on_spawn"), V.HasMetaData(FBlueprintMetadata::MD_ExposeOnSpawn));
		Variables.Add(MakeShared<FJsonValueObject>(J));
	}
	Root->SetArrayField(TEXT("variables"), Variables);

	auto GraphNames = [](const TArray<TObjectPtr<UEdGraph>>& Graphs)
	{
		TArray<TSharedPtr<FJsonValue>> Out;
		for (const UEdGraph* G : Graphs)
		{
			if (G)
			{
				Out.Add(MakeShared<FJsonValueString>(G->GetName()));
			}
		}
		return Out;
	};
	Root->SetArrayField(TEXT("functions"), GraphNames(Blueprint->FunctionGraphs));
	Root->SetArrayField(TEXT("macros"), GraphNames(Blueprint->MacroGraphs));
	TArray<TSharedPtr<FJsonValue>> Events;
	for (const UEdGraph* G : Blueprint->UbergraphPages)
	{
		if (!G)
		{
			continue;
		}
		for (const UEdGraphNode* N : G->Nodes)
		{
			if (const UK2Node_CustomEvent* CE = Cast<UK2Node_CustomEvent>(N))
			{
				Events.Add(MakeShared<FJsonValueString>(CE->CustomFunctionName.ToString()));
			}
			else if (const UK2Node_Event* E = Cast<UK2Node_Event>(N))
			{
				Events.Add(MakeShared<FJsonValueString>(E->GetFunctionName().ToString()));
			}
		}
	}
	Root->SetArrayField(TEXT("events"), Events);
	return MCPJsonToString(Root);
}

FString UMCPAuthoringSubsystem::CheckMemberName(UBlueprint* Blueprint, FName Name)
{
	if (!Blueprint)
	{
		return TEXT("not a Blueprint");
	}
	// Scoped to the skeleton class, as FindUniqueKismetName (which renames a clash) is.
	FKismetNameValidator Validator(Blueprint, NAME_None, Blueprint->SkeletonGeneratedClass);
	switch (Validator.IsValid(Name))
	{
	case EValidatorResult::Ok: return FString();
	case EValidatorResult::EmptyName: return TEXT("the name is empty");
	case EValidatorResult::TooLong: return TEXT("the name is too long");
	case EValidatorResult::ContainsInvalidCharacters: return TEXT("the name contains invalid characters");
	default: return FString::Printf(TEXT("%s is already used by this Blueprint or a class it inherits from"), *Name.ToString());
	}
}

bool UMCPAuthoringSubsystem::RemoveMemberVariable(UBlueprint* Blueprint, FName Name)
{
	if (!Blueprint || FBlueprintEditorUtils::FindNewVariableIndex(Blueprint, Name) == INDEX_NONE)
	{
		return false;
	}
	FBlueprintEditorUtils::RemoveMemberVariable(Blueprint, Name);
	return true;
}

static FString MCPNormName(const FString& Name)
{
	return Name.Replace(TEXT("_"), TEXT("")).ToLower();
}

FString UMCPAuthoringSubsystem::SetConfigDefaultsJson(UClass* SettingsClass, const FString& PropertiesJson)
{
	TSharedRef<FJsonObject> Out = MakeShared<FJsonObject>();
	TArray<TSharedPtr<FJsonValue>> Errors;
	TSharedRef<FJsonObject> Values = MakeShared<FJsonObject>();
	if (!SettingsClass || !SettingsClass->HasAnyClassFlags(CLASS_Config))
	{
		Out->SetBoolField(TEXT("ok"), false);
		Out->SetStringField(TEXT("error"), TEXT("not a config (settings) class"));
		return MCPJsonToString(Out);
	}
	if (!SettingsClass->HasAnyClassFlags(CLASS_DefaultConfig))
	{
		// A per-user config class: a Default*.ini write would land in the wrong layer.
		Out->SetBoolField(TEXT("ok"), false);
		Out->SetStringField(TEXT("error"), TEXT("a per-user config class (not defaultconfig): its settings do not belong in the project's Default*.ini"));
		return MCPJsonToString(Out);
	}
	TSharedPtr<FJsonObject> In;
	const TSharedRef<TJsonReader<>> Reader = TJsonReaderFactory<>::Create(PropertiesJson);
	if (!FJsonSerializer::Deserialize(Reader, In) || !In.IsValid())
	{
		Out->SetBoolField(TEXT("ok"), false);
		Out->SetStringField(TEXT("error"), TEXT("properties must be a JSON object"));
		return MCPJsonToString(Out);
	}
	UObject* Defaults = SettingsClass->GetDefaultObject();
	int32 Set = 0;
	for (const TPair<FString, TSharedPtr<FJsonValue>>& Pair : In->Values)
	{
		auto Error = [&](const FString& Message)
		{
			TSharedRef<FJsonObject> E = MakeShared<FJsonObject>();
			E->SetStringField(TEXT("property"), Pair.Key);
			E->SetStringField(TEXT("error"), Message);
			Errors.Add(MakeShared<FJsonValueObject>(E));
		};
		// Every property the name could mean (case and underscores ignored; a bool's b
		// prefix optional, as Python spells it) — never the first of several.
		TArray<FProperty*> Matches;
		for (TFieldIterator<FProperty> It(SettingsClass); It; ++It)
		{
			const FString Norm = MCPNormName(It->GetName());
			const bool bBoolAlias = CastField<FBoolProperty>(*It) && Norm.StartsWith(TEXT("b")) && Norm.Mid(1) == MCPNormName(Pair.Key);
			if (Norm == MCPNormName(Pair.Key) || bBoolAlias)
			{
				Matches.AddUnique(*It);
			}
		}
		if (Matches.Num() > 1)
		{
			// The exact name wins over a bool's b-less alias (Enabled vs bEnabled).
			for (FProperty* M : Matches)
			{
				if (M->GetName() == Pair.Key)
				{
					Matches = {M};
					break;
				}
			}
		}
		if (Matches.Num() != 1)
		{
			FString Names;
			for (const FProperty* M : Matches)
			{
				Names += (Names.IsEmpty() ? TEXT("") : TEXT(", ")) + M->GetName();
			}
			Error(Matches.Num() == 0 ? FString::Printf(TEXT("%s has no property %s"), *SettingsClass->GetName(), *Pair.Key)
				: FString::Printf(TEXT("%s could mean %s: use the exact name"), *Pair.Key, *Names));
			continue;
		}
		FProperty* Prop = Matches[0];
		if (!Prop->HasAnyPropertyFlags(CPF_Config) || !Prop->HasAnyPropertyFlags(CPF_Edit) || Prop->HasAnyPropertyFlags(CPF_EditConst))
		{
			Error(FString::Printf(TEXT("%s is not an editable config property"), *Prop->GetName()));
			continue;
		}
		// The JSON's type must be the property's: FJsonObjectConverter would turn an object
		// into an empty string, a number into a name... (live R3).
		const EJson Kind = Pair.Value.IsValid() ? Pair.Value->Type : EJson::None;
		const bool bEnum = CastField<FEnumProperty>(Prop) || (CastField<FByteProperty>(Prop) && CastField<FByteProperty>(Prop)->Enum);
		const TCHAR* Expected = nullptr;
		if (CastField<FStrProperty>(Prop) || CastField<FNameProperty>(Prop) || CastField<FTextProperty>(Prop))
		{
			Expected = Kind == EJson::String ? nullptr : TEXT("a string");
		}
		else if (CastField<FBoolProperty>(Prop))
		{
			Expected = Kind == EJson::Boolean ? nullptr : TEXT("true or false");
		}
		else if (bEnum)
		{
			Expected = (Kind == EJson::String || Kind == EJson::Number) ? nullptr : TEXT("an enum name");
		}
		else if (CastField<FNumericProperty>(Prop))
		{
			Expected = Kind == EJson::Number ? nullptr : TEXT("a number");
		}
		if (Expected)
		{
			Error(FString::Printf(TEXT("%s expects %s"), *Prop->GetName(), Expected));
			continue;
		}
		void* Value = Prop->ContainerPtrToValuePtr<void>(Defaults);
		// Convert on a copy: a value that does not fit leaves the property as it was.
		void* Temp = FMemory::Malloc(Prop->GetSize(), Prop->GetMinAlignment());
		Prop->InitializeValue(Temp);
		Prop->CopyCompleteValue(Temp, Value);
		const bool bOk = FJsonObjectConverter::JsonValueToUProperty(Pair.Value, Prop, Temp, 0, 0);
		if (bOk)
		{
			Defaults->PreEditChange(Prop);
			Prop->CopyCompleteValue(Value, Temp);
			FPropertyChangedEvent Changed(Prop, EPropertyChangeType::ValueSet);
			Defaults->PostEditChangeProperty(Changed);
			FString Text;
			Prop->ExportTextItem_Direct(Text, Value, nullptr, nullptr, PPF_None);
			Values->SetStringField(Prop->GetName(), Text);
			++Set;
		}
		Prop->DestroyValue(Temp);
		FMemory::Free(Temp);
		if (!bOk)
		{
			Error(FString::Printf(TEXT("the value does not fit %s (%s)"), *Prop->GetName(), *Prop->GetCPPType()));
		}
	}
	const bool bSaved = Set == 0 || Defaults->TryUpdateDefaultConfigFile();
	Out->SetBoolField(TEXT("ok"), Set > 0 && bSaved);
	if (Set > 0 && !bSaved)
	{
		Out->SetStringField(TEXT("error"), TEXT("the default config file could not be written (read-only?)"));
	}
	Out->SetObjectField(TEXT("values"), Values);
	Out->SetArrayField(TEXT("errors"), Errors);
	return MCPJsonToString(Out);
}
