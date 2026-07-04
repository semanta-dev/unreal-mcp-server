// Copyright unreal-mcp-server. MIT.
#include "MCPHUDWidget.h"
#include "MCPButton.h"

#include "Components/Widget.h"
#include "Components/PanelWidget.h"
#include "Components/CanvasPanelSlot.h"
#include "Blueprint/WidgetTree.h"
#include "Blueprint/WidgetLayoutLibrary.h"
#include "GameFramework/Pawn.h"
#include "GameFramework/PlayerController.h"
#include "GameFramework/PlayerState.h"
#include "Kismet/GameplayStatics.h"
#include "EngineUtils.h" // TActorIterator
#include "UObject/UnrealType.h"
#include "UObject/TextProperty.h"

// ---------------------------------------------------------------------------
// The LIVE-SLATE apply contract: a reflected write to a live sub-widget updates the
// UWidget UPROPERTY but NOT its cached SWidget — UMG only pushes into Slate inside
// SynchronizeProperties(). So every value write is: set the FProperty, then
// SynchronizeProperties() (generic path (a) — one call covers Percent/Text/color).
// ---------------------------------------------------------------------------

void UMCPHUDWidget::ApplyFloat(UWidget* W, FName Field, float V)
{
	if (!W)
	{
		return;
	}
	if (FFloatProperty* P = FindFProperty<FFloatProperty>(W->GetClass(), Field))
	{
		P->SetPropertyValue_InContainer(W, V);
	}
	else if (FDoubleProperty* D = FindFProperty<FDoubleProperty>(W->GetClass(), Field))
	{
		D->SetPropertyValue_InContainer(W, (double)V);
	}
	else
	{
		return;
	}
	W->SynchronizeProperties(); // push to the cached SWidget (else it stays frozen)
}

void UMCPHUDWidget::ApplyText(UWidget* W, FName Field, const FText& V)
{
	if (!W)
	{
		return;
	}
	if (FTextProperty* P = FindFProperty<FTextProperty>(W->GetClass(), Field))
	{
		P->SetPropertyValue_InContainer(W, V);
		W->SynchronizeProperties();
	}
}

void UMCPHUDWidget::SetFieldFloat(FName Widget, FName Field, float Value)
{
	ApplyFloat(ResolveWidget(Widget), Field, Value);
}

void UMCPHUDWidget::SetFieldText(FName Widget, FName Field, const FString& Value)
{
	ApplyText(ResolveWidget(Widget), Field, FText::FromString(Value));
}

void UMCPHUDWidget::SetFieldInt(FName Widget, FName Field, int32 Value)
{
	ApplyText(ResolveWidget(Widget), Field, FText::AsNumber(Value));
}

void UMCPHUDWidget::SetFieldBool(FName Widget, FName Field, bool Value)
{
	// Common bool field is Visibility; also apply as a float 0/1 where numeric.
	if (UWidget* W = ResolveWidget(Widget))
	{
		if (Field == FName(TEXT("Visibility")))
		{
			W->SetVisibility(Value ? ESlateVisibility::Visible : ESlateVisibility::Collapsed);
		}
		else
		{
			ApplyFloat(W, Field, Value ? 1.f : 0.f);
		}
	}
}

UWidget* UMCPHUDWidget::ResolveWidget(FName Name) const
{
	return GetWidgetFromName(Name);
}

UObject* UMCPHUDWidget::ResolveSource(const FMCPFieldSourceBinding& B) const
{
	switch (B.Source)
	{
	case EMCPBindSource::OwningPawn:
		return GetOwningPlayerPawn();
	case EMCPBindSource::OwningPC:
		return GetOwningPlayer();
	case EMCPBindSource::PlayerState:
		return GetOwningPlayer() ? GetOwningPlayer()->PlayerState : nullptr;
	case EMCPBindSource::WorldActor:
	{
		if (UWorld* World = GetWorld())
		{
			for (TActorIterator<AActor> It(World); It; ++It)
			{
#if WITH_EDITOR
				if (It->GetActorLabel() == B.SourceLabel)
#else
				if (It->GetName() == B.SourceLabel)
#endif
				{
					return *It;
				}
			}
		}
		return nullptr;
	}
	case EMCPBindSource::AbilitySystem:
	default:
		return nullptr; // GAS read is a guarded C++ helper (Phase 2); not linked here yet
	}
}

// Read a numeric value from a dotted path (max depth 4): each segment an FProperty
// (object hop or numeric leaf) or, as a fallback, a zero-arg getter UFUNCTION.
bool UMCPHUDWidget::ReadNumericPath(UObject* Src, const FString& Path, double& Out) const
{
	if (!Src || Path.IsEmpty())
	{
		return false;
	}
	TArray<FString> Segments;
	Path.ParseIntoArray(Segments, TEXT("."));
	UObject* Cur = Src;
	for (int32 i = 0; i < Segments.Num(); ++i)
	{
		const FName Seg(*Segments[i]);
		const bool bLast = (i == Segments.Num() - 1);
		if (!Cur)
		{
			return false;
		}
		if (bLast)
		{
			// numeric leaf: FProperty first, then a zero-arg getter.
			if (FNumericProperty* NP = FindFProperty<FNumericProperty>(Cur->GetClass(), Seg))
			{
				const void* Ptr = NP->ContainerPtrToValuePtr<void>(Cur);
				Out = NP->IsFloatingPoint() ? NP->GetFloatingPointPropertyValue(Ptr) : (double)NP->GetSignedIntPropertyValue(Ptr);
				return true;
			}
			if (UFunction* Fn = Cur->FindFunction(Seg))
			{
				if (Fn->NumParms == 1 && Fn->GetReturnProperty())
				{
					uint8* Buf = (uint8*)FMemory_Alloca(Fn->ParmsSize);
					FMemory::Memzero(Buf, Fn->ParmsSize);
					Cur->ProcessEvent(Fn, Buf);
					if (FNumericProperty* RP = CastField<FNumericProperty>(Fn->GetReturnProperty()))
					{
						const void* RPtr = RP->ContainerPtrToValuePtr<void>(Buf);
						Out = RP->IsFloatingPoint() ? RP->GetFloatingPointPropertyValue(RPtr) : (double)RP->GetSignedIntPropertyValue(RPtr);
						return true;
					}
				}
			}
			return false;
		}
		// object hop.
		if (FObjectPropertyBase* OP = FindFProperty<FObjectPropertyBase>(Cur->GetClass(), Seg))
		{
			Cur = OP->GetObjectPropertyValue_InContainer(Cur);
			continue;
		}
		return false;
	}
	return false;
}

void UMCPHUDWidget::NativeConstruct()
{
	Super::NativeConstruct();
	// Bind every UMCPButton child's OnCommand -> RunNamedCommand (no graph authoring).
	if (WidgetTree)
	{
		WidgetTree->ForEachWidget([this](UWidget* W)
		{
			if (UMCPButton* Btn = Cast<UMCPButton>(W))
			{
				Btn->OnCommand.AddUniqueDynamic(this, &UMCPHUDWidget::OnMCPButtonCommand);
			}
		});
	}
}

void UMCPHUDWidget::OnMCPButtonCommand(FName Command)
{
	RunNamedCommand(Command);
}

void UMCPHUDWidget::RunNamedCommand(FName Command)
{
	if (Command == FName(TEXT("Resume")))
	{
		RemoveFromParent();
		if (APlayerController* PC = GetOwningPlayer())
		{
			PC->SetInputMode(FInputModeGameOnly());
			PC->bShowMouseCursor = false;
		}
	}
	else if (Command == FName(TEXT("Quit")))
	{
		UGameplayStatics::OpenLevel(this, FName(*GetWorld()->GetName()));
	}
	// OpenPanel + project commands: override RunNamedCommand.
}

void UMCPHUDWidget::NativeTick(const FGeometry& MyGeometry, float DeltaTime)
{
	Super::NativeTick(MyGeometry, DeltaTime);

	// --- value pulls (health/ammo) ---
	for (const FMCPFieldSourceBinding& B : FieldSourceBindings)
	{
		UObject* Src = ResolveSource(B);
		if (!Src)
		{
			continue; // owning pawn often null before possession — hold last-good
		}
		double Val = 0.0;
		if (!ReadNumericPath(Src, B.Path, Val))
		{
			continue;
		}
		UWidget* Target = ResolveWidget(B.TargetWidget);
		if (!Target)
		{
			continue;
		}
		const bool bRatio = (B.Conversion == EMCPFieldConversion::Ratio) || !B.MaxPath.IsEmpty();
		if (bRatio)
		{
			double MaxVal = 0.0;
			if (ReadNumericPath(Src, B.MaxPath, MaxVal) && MaxVal != 0.0)
			{
				ApplyFloat(Target, B.TargetField, (float)(Val / MaxVal));
			}
			else
			{
				ApplyFloat(Target, B.TargetField, 0.f); // denom 0 -> 0, never inf/NaN
			}
		}
		else if (B.Conversion == EMCPFieldConversion::FormatText)
		{
			double MaxVal = 0.0;
			ReadNumericPath(Src, B.MaxPath, MaxVal);
			FFormatNamedArguments Args;
			Args.Add(TEXT("value"), FText::AsNumber((int32)Val));
			Args.Add(TEXT("max"), FText::AsNumber((int32)MaxVal));
			ApplyText(Target, B.TargetField, FText::Format(FTextFormat::FromString(B.Format), Args));
		}
		else if (B.Conversion == EMCPFieldConversion::IntToText)
		{
			ApplyText(Target, B.TargetField, FText::AsNumber((int32)Val));
		}
		else
		{
			ApplyFloat(Target, B.TargetField, (float)Val);
		}
	}

	// --- world tracking (objective marker), DPI-correct ---
	for (const FMCPWorldTrackBinding& T : WorldTrackBindings)
	{
		AActor* TargetActor = nullptr;
		if (T.TargetLabel.IsEmpty())
		{
			TargetActor = GetOwningPlayerPawn();
		}
		else if (UWorld* World = GetWorld())
		{
			for (TActorIterator<AActor> It(World); It; ++It)
			{
#if WITH_EDITOR
				if (It->GetActorLabel() == T.TargetLabel) { TargetActor = *It; break; }
#else
				if (It->GetName() == T.TargetLabel) { TargetActor = *It; break; }
#endif
			}
		}
		if (!TargetActor)
		{
			continue;
		}
		UWidget* Marker = ResolveWidget(T.MarkerWidget);
		UCanvasPanelSlot* MarkerSlot = Marker ? Cast<UCanvasPanelSlot>(Marker->Slot) : nullptr;
		if (!MarkerSlot)
		{
			continue;
		}
		FVector2D ScreenPos;
		const FVector WorldLoc = TargetActor->GetActorLocation() + T.WorldOffset;
		if (UWidgetLayoutLibrary::ProjectWorldLocationToWidgetPosition(GetOwningPlayer(), WorldLoc, ScreenPos, /*bPlayerViewportRelative=*/false))
		{
			MarkerSlot->SetPosition(ScreenPos); // slot writes push to live Slate directly
		}
	}
}
