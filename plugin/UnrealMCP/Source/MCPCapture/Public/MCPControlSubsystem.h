// Copyright unreal-mcp-server. MIT.
//
// UMCPControlSubsystem synthesizes INPUT into the live game world (possessed PIE /
// standalone) so an agent can actually PLAY a game to test it — press keys, tap
// buttons, hold a movement key for a duration — not just observe it. Python cannot
// do this: there is no unreal.* API to feed the player's input stack, and
// EditorActorSubsystem only touches the editor world. A GameInstanceSubsystem runs
// inside the game world and can drive its PlayerController's InputKey, so injected
// keys flow through the exact same input path a human keypress would (legacy action/
// axis mappings included — WASD held down moves the pawn, SpaceBar taps jump).
//
// Driven by the MCP server via reflection: the bridge's pie_input resolves this
// subsystem from the PIE GameInstance (unreal.MCPControlSubsystem.get(world)) and
// calls InjectKeyByName / TapKey / HoldKey. Keys are named (FKey from FName) so the
// Python side needs no FKey construction.
#pragma once

#include "CoreMinimal.h"
#include "Subsystems/GameInstanceSubsystem.h"
#include "Engine/TimerHandle.h"
#include "MCPControlSubsystem.generated.h"

UCLASS()
class MCPCAPTURE_API UMCPControlSubsystem : public UGameInstanceSubsystem
{
	GENERATED_BODY()

public:
	/** Resolve this subsystem from a world context (bridge calls .get(world)). */
	UFUNCTION(BlueprintCallable, Category = "MCP|Control", meta = (WorldContext = "WorldContext"))
	static UMCPControlSubsystem* Get(const UObject* WorldContext);

	/**
	 * Inject a raw key state into player 0's input. KeyName is a UE key name
	 * ("W", "SpaceBar", "LeftMouseButton", "Gamepad_FaceButton_Bottom").
	 * bPressed=true is IE_Pressed, false is IE_Released. Returns false if there is
	 * no player controller or the key name is invalid.
	 */
	UFUNCTION(BlueprintCallable, Category = "MCP|Control")
	bool InjectKeyByName(const FString& KeyName, bool bPressed);

	/** Press then auto-release a key after a short delay (a button tap / one action). */
	UFUNCTION(BlueprintCallable, Category = "MCP|Control")
	bool TapKey(const FString& KeyName);

	/** Press a key now and auto-release it after DurationSeconds (held movement). */
	UFUNCTION(BlueprintCallable, Category = "MCP|Control")
	bool HoldKey(const FString& KeyName, float DurationSeconds);

	/** Create a UserWidget of WidgetClass for player 0 and add it to the viewport
	 *  (plugin API 4). Returns the live widget, or nullptr without a player controller.
	 *  Python has no CreateWidget, so a HUD cannot otherwise be shown in PIE. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Control")
	class UUserWidget* MountWidget(TSubclassOf<class UUserWidget> WidgetClass, int32 ZOrder);

	/** The live UMG widgets on screen (all UserWidgets, or of WidgetClass) as JSON
	 *  (plugin API 4): [{widget, class, in_viewport, nodes: [{name, class, visible,
	 *  position, size, text?}]}] — geometry in viewport pixels, text for text widgets. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Control")
	FString DescribeLiveWidgets(TSubclassOf<class UUserWidget> WidgetClass) const;

	/** Remove every live widget of WidgetClass this subsystem mounted; returns how many. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Control")
	int32 UnmountWidget(TSubclassOf<class UUserWidget> WidgetClass);

	/** Release every key this subsystem is currently holding (safety / teardown). */
	UFUNCTION(BlueprintCallable, Category = "MCP|Control")
	void ReleaseAll();

	// USubsystem
	virtual void Deinitialize() override;

private:
	UPROPERTY(Transient)
	TArray<TObjectPtr<class UUserWidget>> Mounted;

	class APlayerController* ResolvePC() const;
	bool DispatchKey(const struct FKey& Key, bool bPressed);
	void ScheduleRelease(const FString& KeyName, float DelaySeconds);

	// Keys currently held via HoldKey/TapKey, so ReleaseAll / Deinitialize can
	// clear them and we don't leak a stuck key if PIE ends mid-hold.
	UPROPERTY(Transient)
	TSet<FString> HeldKeys;

	// One pending auto-release timer per key: re-pressing a held key cancels its
	// old release (so a longer hold isn't cut short by an earlier one) and handles
	// are dropped as they fire, so the set can't grow unbounded.
	TMap<FString, FTimerHandle> ReleaseTimers;
};
