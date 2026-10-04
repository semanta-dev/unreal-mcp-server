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
#include "Tickable.h"
#include "MCPControlSubsystem.generated.h"

UCLASS()
class MCPCAPTURE_API UMCPControlSubsystem : public UGameInstanceSubsystem, public FTickableGameObject
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

	/** Inject an analog axis (plugin API 5): KeyName is a 1-D axis key ("MouseX",
	 *  "MouseY", "Gamepad_LeftX", "Gamepad_RightTriggerAxis" …). Value is sent EVERY game
	 *  tick for DurationSeconds — axis input is per frame (a mouse axis is that frame's
	 *  delta). A stick/trigger is returned to 0 at the end. Another call for the same key
	 *  replaces it. Returns "" or why it was refused. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Control")
	FString InjectAxis(const FString& KeyName, float Value, float DurationSeconds);

	/** The current (or last) InjectAxis for KeyName as JSON {active, ticks, total}: how
	 *  many game ticks it sent and their sum — axis input is per frame, so the effect is
	 *  ticks x value, whatever the frame rate. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Control")
	FString GetAxisStatsJson(const FString& KeyName) const;

	/** Cursor input through Slate (plugin API 5), in VIEWPORT pixels (the coordinates
	 *  DescribeLiveWidgets reports), inside the game viewport only. Events come from a
	 *  virtual Slate user: the user's OS cursor is never moved or captured and the game's
	 *  input mode is left alone; the game viewport's cached cursor follows (so
	 *  DeprojectMousePosition sees it) and widgets get real hover/press/release. Each
	 *  returns JSON {ok, viewport:[x,y], screen:[x,y], handled, hit} or {ok:false, error}. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Control")
	FString MoveCursor(float X, float Y);

	/** Move to (X, Y) and press/release Button ("LeftMouseButton" by default). */
	UFUNCTION(BlueprintCallable, Category = "MCP|Control")
	FString ClickAt(float X, float Y, const FString& Button);

	/** Press at (X0, Y0), move to (X1, Y1) over DurationSeconds (one move per tick), release. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Control")
	FString DragCursor(float X0, float Y0, float X1, float Y1, float DurationSeconds, const FString& Button);

	/** Give the game's cursor back to the real mouse: cursor calls PIN the game's cursor
	 *  (what DeprojectMousePosition / GetHitResultUnderCursor read) to their position —
	 *  re-applied at the start of every game tick, since Slate rewrites it from the real
	 *  OS cursor whenever that is over the viewport — until this call or the end of PIE.
	 *  The OS cursor itself is never moved. Returns whether a cursor was pinned. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Control")
	bool ReleaseCursor();

	/** The cursor position (viewport pixels) the player controller read during the last
	 *  game tick (GetMousePosition, sampled after the actors ticked) — what game code saw,
	 *  as opposed to a read between frames. (-1, -1) when it had none. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Control")
	FVector2D GetTickCursor() const { return TickCursor; }

	/** Click the centre of the visible live widget named WidgetName (a widget in any live
	 *  UserWidget's tree, or the UserWidget itself). Refused when no visible widget has
	 *  that name, or several do (names them). */
	UFUNCTION(BlueprintCallable, Category = "MCP|Control")
	FString ClickWidget(const FString& WidgetName, const FString& Button);

	/** Spawn an actor into THIS game world (plugin API 5; Python can spawn only into the
	 *  editor world). Collision: adjusted if possible, always spawned. nullptr on failure. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Control")
	class AActor* SpawnInGame(TSubclassOf<class AActor> ActorClass, FVector Location, FRotator Rotation);

	// USubsystem
	virtual void Initialize(FSubsystemCollectionBase& Collection) override;
	virtual void Deinitialize() override;

	// FTickableGameObject (axis injection, drags)
	virtual void Tick(float DeltaTime) override;
	virtual TStatId GetStatId() const override;
	virtual ETickableTickType GetTickableTickType() const override;
	virtual bool IsTickable() const override;
	/** Releases and drags must finish in a paused game (building while paused is normal). */
	virtual bool IsTickableWhenPaused() const override { return true; }
	virtual UWorld* GetTickableGameObjectWorld() const override { return GetWorld(); }

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

	struct FAxisHold
	{
		float Value = 0.f;
		float Remaining = 0.f;
		bool bZeroNext = false; // a stick/trigger: send its rest value next tick, alone
		int32 Ticks = 0;
		double Total = 0.0;
	};
	TMap<FString, FAxisHold> Axes;
	TMap<FString, FAxisHold> AxisDone; // each key's last finished injection (GetAxisStatsJson)

	struct FDragState
	{
		FVector2D From, To; // viewport pixels
		float Duration = 0.f, Elapsed = 0.f;
		FKey Button;
	};
	TOptional<FDragState> Drag;

	/** A button release sent a few ticks after its press (a press and release in one
	 *  frame can be missed by Enhanced Input). */
	struct FPendingUp
	{
		FVector2D Viewport;
		FKey Button;
		int32 Ticks = 2;
	};
	TArray<FPendingUp> PendingUps;

	/** Viewport pixels -> Slate screen space; false (and why) without a game viewport or
	 *  for a position outside it. */
	bool ViewportToScreen(const FVector2D& Viewport, FVector2D& OutScreen, FString* OutWhy) const;
	FVector2D ScreenToViewport(const FVector2D& Screen) const;
	/** Kind: 0 move, 1 press, 2 release. bHeld: Button is down during a move. Routes to
	 *  the widget path under Screen as this subsystem's virtual Slate user; OutHit gets
	 *  the Slate type of the widget hit. Returns whether a widget handled the event. */
	bool SendPointer(const FVector2D& Viewport, const FKey& Button, int32 Kind, bool bHeld, FString* OutHit = nullptr,
		bool* OutGameViewport = nullptr, FString* OutRefusal = nullptr);
	/** Press at Screen; a UI widget's click completes at once (Slate needs no frame), a
	 *  press the game viewport takes is released two ticks later (game input samples per frame). */
	bool Click(const FVector2D& Viewport, const FKey& Button, FString& OutHit, FString& OutRefusal);

	FString PointerResult(const FVector2D& Viewport, bool bHandled, const FString& Hit, const FString& Widget = FString()) const;

	/** The virtual Slate user the pointer events come from: its hover, press and capture
	 *  are its own, so the real cursor (and whatever has captured it) never interferes. */
	TSharedPtr<class FSlateVirtualUserHandle> VirtualUser;

	/** Where the agent put the game's cursor (viewport pixels: a moved or resized window
	 *  keeps the same game pixel), re-applied each tick. */
	TOptional<FVector2D> PinnedViewport;
	FDelegateHandle WorldTickStartHandle;
	FDelegateHandle PostActorTickHandle;
	FVector2D TickCursor = FVector2D(-1.0, -1.0);
	void OnWorldTickStart(UWorld* World, ELevelTick TickType, float DeltaSeconds);
	void OnPostActorTick(UWorld* World, ELevelTick TickType, float DeltaSeconds);
	static constexpr int32 VirtualUserIndex = 9;
};
