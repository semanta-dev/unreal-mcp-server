// Copyright unreal-mcp-server. MIT.
#pragma once

#include "CoreMinimal.h"
#include "Blueprint/UserWidget.h"
#include "MCPHUDWidget.generated.h"

// Where a bound value is read from (HUD_TOOLING_PLAN.md §3.0.1).
UENUM()
enum class EMCPBindSource : uint8
{
	OwningPawn,
	OwningPC,
	PlayerState,
	WorldActor,
	AbilitySystem,
	// Appended (plugin API 4) — never reorder: values are serialized in widget CDOs.
	GameState,  // the world's GameState
	Subsystem,  // SourceLabel = a World/GameInstance/LocalPlayer subsystem class path
};

// How a read value is converted before it is applied to the target field.
UENUM()
enum class EMCPFieldConversion : uint8
{
	None,
	Ratio,          // value = read(Path) / read(MaxPath)
	IntToText,      // FText::AsNumber(int)
	FloatToText,
	FloatToPercent, // value*100 + "%"
	BoolToVisibility,
	FormatText,     // FText::Format(Format, {value},{max})
};

// A per-tick pull binding — the configured, gameplay-tracking value binding.
USTRUCT()
struct FMCPFieldSourceBinding
{
	GENERATED_BODY()

	UPROPERTY() FName TargetWidget;
	UPROPERTY() FName TargetField;
	UPROPERTY() EMCPBindSource Source = EMCPBindSource::OwningPawn;
	UPROPERTY() FString SourceLabel;   // WorldActor: actor label; Subsystem: subsystem class path
	UPROPERTY() FString Path;          // numerator/value path OR a zero-arg getter
	UPROPERTY() FString MaxPath;       // optional denominator
	UPROPERTY() FName Attribute;       // AbilitySystem numeric attribute
	UPROPERTY() FName MaxAttribute;    // AbilitySystem denominator
	UPROPERTY() EMCPFieldConversion Conversion = EMCPFieldConversion::None;
	UPROPERTY() FString Format;        // FormatText only
};

// An objective-marker binding — DPI-correct per-tick world→widget projection.
USTRUCT()
struct FMCPWorldTrackBinding
{
	GENERATED_BODY()

	UPROPERTY() FName MarkerWidget;
	UPROPERTY() FString TargetLabel;   // world actor label; empty = owning pawn
	UPROPERTY() FVector WorldOffset = FVector::ZeroVector;
};

/**
 * UMCPHUDWidget — the reusable HUD base an agent binds to gameplay without authoring a
 * graph (HUD_TOOLING_PLAN.md §3.0.1). OPT-IN (not the widget_create default). Push
 * setters (interim binding, called via widget_set_fields) and per-tick pull config
 * (FieldSourceBindings / WorldTrackBindings, written to the generated-class CDO by
 * widget_bind_field / widget_track_actor) are both applied through the LIVE-SLATE
 * contract — a reflected write + SynchronizeProperties() — so the on-screen widget
 * actually updates rather than only the UPROPERTY (which would false-pass the oracle).
 */
UCLASS()
class MCPCAPTURE_API UMCPHUDWidget : public UUserWidget
{
	GENERATED_BODY()

public:
	// --- push setters (explicit Widget + Field, no per-type guess table) ---
	UFUNCTION(BlueprintCallable, Category = "MCP") void SetFieldFloat(FName Widget, FName Field, float Value);
	UFUNCTION(BlueprintCallable, Category = "MCP") void SetFieldText(FName Widget, FName Field, const FString& Value);
	UFUNCTION(BlueprintCallable, Category = "MCP") void SetFieldInt(FName Widget, FName Field, int32 Value);
	UFUNCTION(BlueprintCallable, Category = "MCP") void SetFieldBool(FName Widget, FName Field, bool Value);

	// --- per-tick pull config (persisted on the generated-class CDO) ---
	UPROPERTY(EditAnywhere, Category = "MCP") TArray<FMCPFieldSourceBinding> FieldSourceBindings;
	UPROPERTY(EditAnywhere, Category = "MCP") TArray<FMCPWorldTrackBinding> WorldTrackBindings;

	// Menu command dispatch. Default handles Resume/Quit; override for project commands.
	UFUNCTION(BlueprintCallable, Category = "MCP") virtual void RunNamedCommand(FName Command);

protected:
	virtual void NativeConstruct() override;
	virtual void NativeTick(const FGeometry& MyGeometry, float DeltaTime) override;

private:
	UWidget* ResolveWidget(FName Name) const;
	UObject* ResolveSource(const FMCPFieldSourceBinding& B) const;
	bool ReadNumericPath(UObject* Src, const FString& Path, double& Out) const;
	void ApplyFloat(UWidget* W, FName Field, float V);
	void ApplyText(UWidget* W, FName Field, const FText& V);

	UFUNCTION() void OnMCPButtonCommand(FName Command);

	// one-shot issue de-dup (SOURCE_NULL / RATIO_DENOM_ZERO hold-last-good)
	TSet<FName> IssuedOnce;
};
