// Copyright unreal-mcp-server. MIT.
#include "MCPAuthoringSubsystem.h"

#include "WidgetBlueprint.h"
#include "Blueprint/WidgetTree.h"
#include "Components/Widget.h"
#include "Components/PanelWidget.h"
#include "Blueprint/UserWidget.h"
#include "Kismet2/KismetEditorUtilities.h"
#include "Kismet2/CompilerResultsLog.h"
#include "UObject/UnrealType.h"
#include "Serialization/JsonSerializer.h"
#include "Dom/JsonObject.h"

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
