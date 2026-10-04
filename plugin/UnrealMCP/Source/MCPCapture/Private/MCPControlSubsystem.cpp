// Copyright unreal-mcp-server. MIT.
#include "MCPControlSubsystem.h"
#include "Blueprint/UserWidget.h"
#include "Blueprint/SlateBlueprintLibrary.h"
#include "Serialization/JsonWriter.h"
#include "Serialization/JsonSerializer.h"
#include "Policies/CondensedJsonPrintPolicy.h"
#include "Dom/JsonObject.h"
#include "Components/EditableTextBox.h"
#include "Components/PanelWidget.h"
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
#include "Engine/GameViewportClient.h"
#include "Framework/Application/SlateApplication.h"
#include "Layout/WidgetPath.h"
#include "Widgets/SViewport.h"
#include "Widgets/SWindow.h"
#include "Framework/Application/SlateUser.h"
#include "Misc/ScopeExit.h"
#include "GenericPlatform/GenericPlatformInputDeviceMapper.h"

// A mouse axis is a per-frame delta; a stick or trigger holds its last value.
static bool IsMouseDeltaAxis(const FKey& Key)
{
	return Key == EKeys::MouseX || Key == EKeys::MouseY || Key == EKeys::MouseWheelAxis;
}

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

void UMCPControlSubsystem::ScheduleRelease(const FString& KeyName, float DelaySeconds, bool bHold)
{
	// One pending release per key: re-pressing/extending a hold replaces the earlier one
	// (a longer hold is not cut short). Counted in Tick, which runs while the game is
	// paused too: a tap is released on time (TimerManager is paused); a hold, like an
	// axis hold, is game time and waits while the game is paused.
	KeyReleases.Add(KeyName, FKeyRelease{FMath::Max(DelaySeconds, 0.01f), bHold});
}

bool UMCPControlSubsystem::TapKey(const FString& KeyName)
{
	// Press now; release next frame-ish so the game samples the pressed state.
	if (!InjectKeyByName(KeyName, true))
	{
		return false;
	}
	ScheduleRelease(KeyName, 0.05f, /*bHold=*/false);
	return true;
}

bool UMCPControlSubsystem::HoldKey(const FString& KeyName, float DurationSeconds)
{
	if (!InjectKeyByName(KeyName, true))
	{
		return false;
	}
	ScheduleRelease(KeyName, DurationSeconds, /*bHold=*/true);
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
	KeyReleases.Reset();
	// Axes: a mouse delta just stops; a stick/trigger returns to rest on the next tick,
	// alone in its frame (this frame may already have had its value).
	for (auto It = Axes.CreateIterator(); It; ++It)
	{
		if (IsMouseDeltaAxis(FKey(FName(*It.Key()))))
		{
			AxisDone.Add(It.Key(), It.Value());
			It.RemoveCurrent();
		}
		else
		{
			It.Value().bZeroNext = true;
		}
	}
	if (Drag.IsSet())
	{
		PendingUps.Add({Drag->To, Drag->Button, 0});
		Drag.Reset();
	}
	const TArray<FPendingUp> Ups = MoveTemp(PendingUps);
	PendingUps.Reset();
	for (const FPendingUp& Up : Ups)
	{
		SendPointer(Up.Viewport, Up.Button, 2, false);
	}
}

void UMCPControlSubsystem::Initialize(FSubsystemCollectionBase& Collection)
{
	Super::Initialize(Collection);
	WorldTickStartHandle = FWorldDelegates::OnWorldTickStart.AddUObject(this, &UMCPControlSubsystem::OnWorldTickStart);
	PostActorTickHandle = FWorldDelegates::OnWorldPostActorTick.AddUObject(this, &UMCPControlSubsystem::OnPostActorTick);
}

void UMCPControlSubsystem::OnPostActorTick(UWorld* World, ELevelTick TickType, float DeltaSeconds)
{
	if (World != GetWorld())
	{
		return;
	}
	float X = -1.f, Y = -1.f;
	APlayerController* PC = ResolvePC();
	TickCursor = (PC && PC->GetMousePosition(X, Y)) ? FVector2D(X, Y) : FVector2D(-1.0, -1.0);
}

void UMCPControlSubsystem::OnWorldTickStart(UWorld* World, ELevelTick TickType, float DeltaSeconds)
{
	// Before the player controller reads input this frame: the game's cursor is the
	// agent's — where it last put it, mid-drag too (the frame that reads a drag's press
	// must see its start, not the real cursor).
	if (World == GetWorld() && PinnedViewport.IsSet())
	{
		SendPointer(PinnedViewport.GetValue(), Drag.IsSet() ? Drag->Button : EKeys::Invalid, 0, Drag.IsSet());
	}
}

bool UMCPControlSubsystem::ReleaseCursor()
{
	const bool bWasPinned = PinnedViewport.IsSet();
	PinnedViewport.Reset();
	return bWasPinned;
}

void UMCPControlSubsystem::Deinitialize()
{
	FWorldDelegates::OnWorldTickStart.Remove(WorldTickStartHandle);
	FWorldDelegates::OnWorldPostActorTick.Remove(PostActorTickHandle);
	PinnedViewport.Reset();
	ReleaseAll();
	// The handle unregisters the virtual user when its last reference goes (another PIE
	// client may still hold it): never unregister it here.
	VirtualUser.Reset();
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
		// Bounded: a menu-heavy game can hold thousands of live widgets.
		constexpr int32 MaxNodes = 2000;
		int32 Total = 0;
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
			bool bTruncated = false;
			W->WidgetTree->ForEachWidget([&Nodes, &Total, &bTruncated, W](UWidget* N)
			{
				if (!N)
				{
					return;
				}
				if (Total >= MaxNodes)
				{
					bTruncated = true;
					return;
				}
				++Total;
				TSharedRef<FJsonObject> NJ = MakeShared<FJsonObject>();
				NJ->SetStringField(TEXT("name"), N->GetName());
				NJ->SetStringField(TEXT("class"), N->GetClass()->GetName());
				const UPanelWidget* Parent = N->GetParent();
				NJ->SetStringField(TEXT("parent"), Parent ? Parent->GetName() : FString());
				// visible: on screen as far as visibility goes — the node, every ancestor
				// panel and the user widget itself (a child of a collapsed panel is hidden).
				bool bShown = N->IsVisible() && W->IsVisible();
				for (const UWidget* A = Parent; A && bShown; A = A->GetParent())
				{
					bShown = A->IsVisible();
				}
				NJ->SetBoolField(TEXT("visible"), bShown);
				NJ->SetBoolField(TEXT("own_visible"), N->IsVisible());
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
			if (bTruncated)
			{
				J->SetBoolField(TEXT("truncated"), true);
			}
			Out.Add(MakeShared<FJsonValueObject>(J));
		}
	}
	FString S;
	TSharedRef<TJsonWriter<>> Wr = TJsonWriterFactory<>::Create(&S);
	FJsonSerializer::Serialize(Out, Wr);
	return S;
}

// --- API 5: per-tick axis injection --------------------------------------------------

FString UMCPControlSubsystem::InjectAxis(const FString& KeyName, float Value, float DurationSeconds)
{
	const FKey Key = FKey(FName(*KeyName));
	if (!Key.IsValid())
	{
		return FString::Printf(TEXT("unknown key '%s'"), *KeyName);
	}
	if (!Key.IsAxis1D())
	{
		return FString::Printf(TEXT("'%s' is not a 1-D axis key (MouseX, MouseY, Gamepad_LeftX, Gamepad_RightTriggerAxis ...)"), *KeyName);
	}
	if (!FMath::IsFinite(Value) || !FMath::IsFinite(DurationSeconds) || DurationSeconds <= 0.f)
	{
		return TEXT("value must be finite and duration_s > 0");
	}
	if (!ResolvePC())
	{
		return TEXT("no player controller in the game world");
	}
	FAxisHold& Hold = Axes.FindOrAdd(KeyName);
	Hold = FAxisHold();
	Hold.Value = Value;
	Hold.Remaining = DurationSeconds;
	return FString();
}

FString UMCPControlSubsystem::GetAxisStatsJson(const FString& KeyName) const
{
	TSharedRef<FJsonObject> O = MakeShared<FJsonObject>();
	const FAxisHold* Live = Axes.Find(KeyName);
	const FAxisHold* Done = AxisDone.Find(KeyName);
	const FAxisHold* H = Live ? Live : Done;
	O->SetBoolField(TEXT("active"), Live != nullptr);
	O->SetNumberField(TEXT("ticks"), H ? H->Ticks : 0);
	O->SetNumberField(TEXT("total"), H ? H->Total : 0.0);
	FString S;
	TSharedRef<TJsonWriter<TCHAR, TCondensedJsonPrintPolicy<TCHAR>>> W = TJsonWriterFactory<TCHAR, TCondensedJsonPrintPolicy<TCHAR>>::Create(&S);
	FJsonSerializer::Serialize(O, W);
	return S;
}

TStatId UMCPControlSubsystem::GetStatId() const
{
	RETURN_QUICK_DECLARE_CYCLE_STAT(UMCPControlSubsystem, STATGROUP_Tickables);
}

ETickableTickType UMCPControlSubsystem::GetTickableTickType() const
{
	return HasAnyFlags(RF_ClassDefaultObject) ? ETickableTickType::Never : ETickableTickType::Conditional;
}

bool UMCPControlSubsystem::IsTickable() const
{
	return Axes.Num() > 0 || Drag.IsSet() || PendingUps.Num() > 0 || KeyReleases.Num() > 0;
}

void UMCPControlSubsystem::Tick(float DeltaTime)
{
	APlayerController* PC = ResolvePC();
	const UWorld* World = GetWorld();
	const bool bPaused = World && World->IsPaused();
	for (auto It = KeyReleases.CreateIterator(); It; ++It)
	{
		if (bPaused && It.Value().bHold)
		{
			continue;
		}
		It.Value().Remaining -= DeltaTime;
		if (It.Value().Remaining <= 0.f)
		{
			const FString Key = It.Key();
			It.RemoveCurrent();
			InjectKeyByName(Key, false);
		}
	}
	for (auto It = Axes.CreateIterator(); It; ++It)
	{
		const FKey Key = FKey(FName(*It.Key()));
		FAxisHold& H = It.Value();
		if (!PC)
		{
			It.RemoveCurrent();
			continue;
		}
		if (H.bZeroNext)
		{
			// Alone in its frame: an axis's value is the sum of the frame's samples, and a
			// stick/trigger keeps its last value when a frame has none — so the zero must not
			// share a frame with the last value.
			PC->InputKey(FInputKeyEventArgs::CreateSimulated(Key, IE_Axis, 0.f, 1));
			AxisDone.Add(It.Key(), H);
			It.RemoveCurrent();
			continue;
		}
		if (bPaused)
		{
			continue; // a hold is game time: it waits while the game is paused
		}
		// This tick's value: one frame's worth of axis input (a mouse axis is a delta).
		PC->InputKey(FInputKeyEventArgs::CreateSimulated(Key, IE_Axis, H.Value, /*NumSamples=*/1));
		H.Ticks += 1;
		H.Total += H.Value;
		H.Remaining -= DeltaTime;
		if (H.Remaining <= 0.f)
		{
			if (IsMouseDeltaAxis(Key))
			{
				AxisDone.Add(It.Key(), H);
				It.RemoveCurrent();
			}
			else
			{
				H.bZeroNext = true; // a stick/trigger returns to rest next tick
			}
		}
	}
	if (Drag.IsSet())
	{
		FDragState& D = Drag.GetValue();
		D.Elapsed += DeltaTime;
		const float A = D.Duration > 0.f ? FMath::Clamp(D.Elapsed / D.Duration, 0.f, 1.f) : 1.f;
		const FVector2D At = FMath::Lerp(D.From, D.To, (double)A);
		SendPointer(At, D.Button, 0, true);
		PinnedViewport = At;
		if (A >= 1.f)
		{
			PendingUps.Add({D.To, D.Button, 1});
			Drag.Reset();
		}
	}
	for (int32 i = PendingUps.Num() - 1; i >= 0; --i)
	{
		if (--PendingUps[i].Ticks <= 0)
		{
			const FPendingUp Up = PendingUps[i];
			PendingUps.RemoveAt(i);
			SendPointer(Up.Viewport, Up.Button, 2, false);
		}
	}
}

// --- API 5: cursor through Slate -------------------------------------------------------

bool UMCPControlSubsystem::ViewportToScreen(const FVector2D& Viewport, FVector2D& OutScreen, FString* OutWhy) const
{
	UWorld* World = GetWorld();
	UGameViewportClient* GVC = World ? World->GetGameViewport() : nullptr;
	TSharedPtr<SViewport> Widget = GVC ? GVC->GetGameViewportWidget() : nullptr;
	FVector2D Pixels(0.0, 0.0);
	if (GVC)
	{
		GVC->GetViewportSize(Pixels);
	}
	const FGeometry& G = Widget.IsValid() ? Widget->GetTickSpaceGeometry() : FGeometry();
	const FVector2D Local(G.GetLocalSize());
	if (!Widget.IsValid() || Pixels.X <= 0.0 || Pixels.Y <= 0.0 || Local.X <= 0.0 || Local.Y <= 0.0)
	{
		if (OutWhy)
		{
			*OutWhy = TEXT("no game viewport");
		}
		return false;
	}
	// Only the game: a position off the viewport would land on the editor's own UI.
	if (Viewport.X < 0.0 || Viewport.Y < 0.0 || Viewport.X >= Pixels.X || Viewport.Y >= Pixels.Y)
	{
		if (OutWhy)
		{
			*OutWhy = FString::Printf(TEXT("(%g, %g) is outside the game viewport (%g x %g pixels)"), Viewport.X, Viewport.Y, Pixels.X, Pixels.Y);
		}
		return false;
	}
	// Viewport pixels are the viewport widget's local space times the DPI scale.
	OutScreen = FVector2D(G.LocalToAbsolute(Viewport * (Local / Pixels)));
	return true;
}

FVector2D UMCPControlSubsystem::ScreenToViewport(const FVector2D& Screen) const
{
	UWorld* World = GetWorld();
	UGameViewportClient* GVC = World ? World->GetGameViewport() : nullptr;
	TSharedPtr<SViewport> Widget = GVC ? GVC->GetGameViewportWidget() : nullptr;
	if (!Widget.IsValid())
	{
		return FVector2D(-1.0, -1.0);
	}
	const FGeometry& G = Widget->GetTickSpaceGeometry();
	FVector2D Pixels(0.0, 0.0);
	GVC->GetViewportSize(Pixels);
	const FVector2D Local(G.GetLocalSize());
	if (Local.X <= 0.0 || Local.Y <= 0.0)
	{
		return FVector2D(-1.0, -1.0);
	}
	return FVector2D(G.AbsoluteToLocal(Screen)) * (Pixels / Local);
}

bool UMCPControlSubsystem::SendPointer(const FVector2D& Viewport, const FKey& Button, int32 Kind, bool bHeld, FString* OutHit,
	bool* OutGameViewport, FString* OutRefusal)
{
	if (!FSlateApplication::IsInitialized())
	{
		if (OutRefusal)
		{
			*OutRefusal = TEXT("Slate is not running");
		}
		return false;
	}
	FVector2D Screen;
	FString Why;
	if (!ViewportToScreen(Viewport, Screen, &Why))
	{
		if (Kind == 2)
		{
			return ReleaseOnViewport(Viewport, Button);
		}
		if (OutRefusal)
		{
			*OutRefusal = Why;
		}
		return false;
	}
	FSlateApplication& App = FSlateApplication::Get();
	if (!VirtualUser.IsValid())
	{
		VirtualUser = App.FindOrCreateVirtualUser(VirtualUserIndex);
	}
	const int32 UserIndex = VirtualUser->GetUserIndex();
	// Route down an explicit path (as UWidgetInteractionComponent does), not through
	// ProcessMouse*Event: input pre-processors and the real cursor's capture are bypassed,
	// and the OS cursor is never moved or captured.
	const FWidgetPath Path = App.LocateWindowUnderMouse(Screen, App.GetInteractiveTopLevelWindows(), false, UserIndex);
	UWorld* World = GetWorld();
	UGameViewportClient* GVC = World ? World->GetGameViewport() : nullptr;
	const TSharedPtr<SViewport> GameViewport = GVC ? GVC->GetGameViewportWidget() : nullptr;
	if (!Path.IsValid() || !GameViewport.IsValid() || !Path.ContainsWidget(GameViewport.Get()))
	{
		if (Kind == 2)
		{
			// Whatever covers the point now, the button must come up: never leave it down.
			return ReleaseOnViewport(Viewport, Button);
		}
		// Something that is not the game (an editor menu, a dialog) is on top there.
		if (OutRefusal)
		{
			*OutRefusal = FString::Printf(TEXT("(%g, %g) is covered by %s, not the game"), Viewport.X, Viewport.Y,
				Path.IsValid() ? *Path.Widgets.Last().Widget->GetTypeAsString() : TEXT("nothing"));
		}
		return false;
	}
	TSet<FKey> Pressed;
	if (Kind == 1 || (Kind == 0 && bHeld))
	{
		Pressed.Add(Button);
	}
	// The player's own input device: a click that reaches the game viewport is player 0's
	// mouse button, and the viewport caches this position as the cursor (only the primary
	// platform user's device updates it) — what DeprojectMousePosition reads. Zero cursor
	// delta (last = this position): a move never turns a mouse-look camera (that is
	// pie op=input action=axis). No modifier keys: the user's real ones never leak in.
	// FPointerEvent holds PressedButtons by pointer: Pressed outlives the event.
	const FPointerEvent Event(IPlatformInputDeviceMapper::Get().GetDefaultInputDevice(), /*PointerIndex=*/0, Screen, Screen,
		Pressed, Kind == 0 ? EKeys::Invalid : Button, 0.f, FModifierKeysState(), UserIndex);
	// Slate grants mouse capture only while the application is active (live R2: with the
	// editor in the background a button's press took no capture, so its release never
	// clicked). For this synthetic event only, handle input as if it were — then restore.
	const bool bWasHandlingInactive = App.GetHandleDeviceInputWhenApplicationNotActive();
	App.SetHandleDeviceInputWhenApplicationNotActive(true);
	ON_SCOPE_EXIT
	{
		App.SetHandleDeviceInputWhenApplicationNotActive(bWasHandlingInactive);
	};
	if (OutHit)
	{
		*OutHit = Path.Widgets.Last().Widget->GetTypeAsString();
	}
	switch (Kind)
	{
	case 1:
	{
		const FReply Reply = App.RoutePointerDownEvent(Path, Event);
		if (OutGameViewport)
		{
			// Who took the press: the game viewport (or nobody) vs a UI widget above it.
			const TSharedPtr<SWidget> Handler = Reply.GetHandler();
			*OutGameViewport = !Handler.IsValid() || Handler == StaticCastSharedPtr<SWidget>(GameViewport);
		}
		return Reply.IsEventHandled();
	}
	case 2:
		return App.RoutePointerUpEvent(Path, Event).IsEventHandled();
	default:
		return App.RoutePointerMoveEvent(Path, Event, /*bIsSynthetic=*/false);
	}
}

bool UMCPControlSubsystem::ReleaseOnViewport(const FVector2D& Viewport, const FKey& Button)
{
	// A release whose point is no longer the game's (a toast window over it, a window
	// resized smaller than a drag's end): route it to the game viewport itself, at the
	// nearest viewport pixel. A widget holding the virtual user's capture gets it anyway
	// (Slate routes a release to its captor).
	UWorld* World = GetWorld();
	UGameViewportClient* GVC = World ? World->GetGameViewport() : nullptr;
	const TSharedPtr<SViewport> GameViewport = GVC ? GVC->GetGameViewportWidget() : nullptr;
	if (!GameViewport.IsValid() || !VirtualUser.IsValid() || !FSlateApplication::IsInitialized())
	{
		return false;
	}
	FSlateApplication& App = FSlateApplication::Get();
	FVector2D Pixels(0.0, 0.0);
	GVC->GetViewportSize(Pixels);
	const FVector2D Clamped(FMath::Clamp(Viewport.X, 0.0, FMath::Max(Pixels.X - 1.0, 0.0)), FMath::Clamp(Viewport.Y, 0.0, FMath::Max(Pixels.Y - 1.0, 0.0)));
	FVector2D Screen;
	FWidgetPath Path;
	if (!ViewportToScreen(Clamped, Screen, nullptr) || !App.FindPathToWidget(GameViewport.ToSharedRef(), Path, EVisibility::All))
	{
		return false;
	}
	const TSet<FKey> NonePressed;
	const FPointerEvent Event(IPlatformInputDeviceMapper::Get().GetDefaultInputDevice(), /*PointerIndex=*/0, Screen, Screen,
		NonePressed, Button, 0.f, FModifierKeysState(), VirtualUser->GetUserIndex());
	const bool bWasHandlingInactive = App.GetHandleDeviceInputWhenApplicationNotActive();
	App.SetHandleDeviceInputWhenApplicationNotActive(true);
	ON_SCOPE_EXIT
	{
		App.SetHandleDeviceInputWhenApplicationNotActive(bWasHandlingInactive);
	};
	return App.RoutePointerUpEvent(Path, Event).IsEventHandled();
}

// Pointer results are JSON the companion parses: built with the engine's serializer
// (FString::ReplaceCharWithEscapedChar is C escaping: a backslash-quote is not valid JSON).
static FString CondensedJson(const TSharedRef<FJsonObject>& Object)
{
	FString Out;
	TSharedRef<TJsonWriter<TCHAR, TCondensedJsonPrintPolicy<TCHAR>>> Writer = TJsonWriterFactory<TCHAR, TCondensedJsonPrintPolicy<TCHAR>>::Create(&Out);
	FJsonSerializer::Serialize(Object, Writer);
	return Out;
}

FString UMCPControlSubsystem::PointerResult(const FVector2D& Viewport, bool bHandled, const FString& Hit, const FString& Widget) const
{
	FVector2D Screen = FVector2D::ZeroVector;
	ViewportToScreen(Viewport, Screen, nullptr);
	TSharedRef<FJsonObject> O = MakeShared<FJsonObject>();
	O->SetBoolField(TEXT("ok"), true);
	O->SetArrayField(TEXT("viewport"), {MakeShared<FJsonValueNumber>(Viewport.X), MakeShared<FJsonValueNumber>(Viewport.Y)});
	O->SetArrayField(TEXT("screen"), {MakeShared<FJsonValueNumber>(FMath::RoundToDouble(Screen.X * 10.0) / 10.0), MakeShared<FJsonValueNumber>(FMath::RoundToDouble(Screen.Y * 10.0) / 10.0)});
	O->SetBoolField(TEXT("handled"), bHandled);
	O->SetStringField(TEXT("hit"), Hit);
	if (!Widget.IsEmpty())
	{
		O->SetStringField(TEXT("widget"), Widget);
	}
	return CondensedJson(O);
}

static FString PointerError(const FString& Message)
{
	TSharedRef<FJsonObject> O = MakeShared<FJsonObject>();
	O->SetBoolField(TEXT("ok"), false);
	O->SetStringField(TEXT("error"), Message);
	return CondensedJson(O);
}

static FKey MouseButtonOf(const FString& Button)
{
	return FKey(FName(*(Button.IsEmpty() ? FString(TEXT("LeftMouseButton")) : Button)));
}

bool UMCPControlSubsystem::Click(const FVector2D& Viewport, const FKey& Button, FString& OutHit, FString& OutRefusal)
{
	if (!SendPointer(Viewport, Button, 0, false, nullptr, nullptr, &OutRefusal) && !OutRefusal.IsEmpty())
	{
		return false;
	}
	bool bGameViewport = true;
	const bool bHandled = SendPointer(Viewport, Button, 1, false, &OutHit, &bGameViewport, &OutRefusal);
	if (!OutRefusal.IsEmpty())
	{
		return false;
	}
	if (bGameViewport)
	{
		PendingUps.Add({Viewport, Button, 2});
	}
	else
	{
		SendPointer(Viewport, Button, 2, false);
	}
	PinnedViewport = Viewport;
	return bHandled;
}

FString UMCPControlSubsystem::MoveCursor(float X, float Y)
{
	const FVector2D At(X, Y);
	FString Hit, Refusal;
	const bool bHandled = SendPointer(At, EKeys::Invalid, 0, false, &Hit, nullptr, &Refusal);
	if (!Refusal.IsEmpty())
	{
		return PointerError(Refusal);
	}
	PinnedViewport = At;
	return PointerResult(At, bHandled, Hit);
}

FString UMCPControlSubsystem::ClickAt(float X, float Y, const FString& Button)
{
	const FKey Key = MouseButtonOf(Button);
	if (!Key.IsMouseButton())
	{
		return PointerError(FString::Printf(TEXT("'%s' is not a mouse button"), *Button));
	}
	const FVector2D At(X, Y);
	FString Hit, Refusal;
	const bool bHandled = Click(At, Key, Hit, Refusal);
	if (!Refusal.IsEmpty())
	{
		return PointerError(Refusal);
	}
	return PointerResult(At, bHandled, Hit);
}

FString UMCPControlSubsystem::DragCursor(float X0, float Y0, float X1, float Y1, float DurationSeconds, const FString& Button)
{
	const FKey Key = MouseButtonOf(Button);
	if (!Key.IsMouseButton())
	{
		return PointerError(FString::Printf(TEXT("'%s' is not a mouse button"), *Button));
	}
	if (Drag.IsSet())
	{
		return PointerError(TEXT("a drag is already in progress"));
	}
	const FVector2D From(X0, Y0), To(X1, Y1);
	FVector2D Unused;
	FString Why;
	if (!ViewportToScreen(To, Unused, &Why))
	{
		return PointerError(Why);
	}
	FString Hit, Refusal;
	SendPointer(From, Key, 0, false, nullptr, nullptr, &Refusal);
	const bool bHandled = Refusal.IsEmpty() && SendPointer(From, Key, 1, false, &Hit, nullptr, &Refusal);
	if (!Refusal.IsEmpty())
	{
		return PointerError(Refusal);
	}
	FDragState D;
	D.From = From;
	D.To = To;
	D.Duration = FMath::Max(DurationSeconds, 0.f);
	D.Button = Key;
	Drag = D;
	PinnedViewport = From; // Tick moves it towards To
	return PointerResult(From, bHandled, Hit);
}

FString UMCPControlSubsystem::ClickWidget(const FString& WidgetName, const FString& Button)
{
	const FKey Key = MouseButtonOf(Button);
	if (!Key.IsMouseButton())
	{
		return PointerError(FString::Printf(TEXT("'%s' is not a mouse button"), *Button));
	}
	UWorld* World = GetWorld();
	if (!World || !FSlateApplication::IsInitialized())
	{
		return PointerError(TEXT("no game world"));
	}
	FSlateApplication& App = FSlateApplication::Get();
	TArray<UUserWidget*> Live;
	UWidgetBlueprintLibrary::GetAllWidgetsOfClass(World, Live, UUserWidget::StaticClass(), /*TopLevelOnly=*/false);
	// Every VISIBLE widget with that name: on screen means Slate has a visible path to it.
	TArray<TPair<UWidget*, FGeometry>> Matches;
	TSet<UWidget*> Seen;
	auto Consider = [&](UWidget* W)
	{
		if (!W || Seen.Contains(W))
		{
			return;
		}
		Seen.Add(W);
		TSharedPtr<SWidget> SW = W->GetCachedWidget();
		FWidgetPath Path;
		if (SW.IsValid() && App.FindPathToWidget(SW.ToSharedRef(), Path, EVisibility::Visible) && Path.IsValid())
		{
			const FGeometry G = Path.Widgets.Last().Geometry;
			if (G.GetLocalSize().X > 0.f && G.GetLocalSize().Y > 0.f)
			{
				Matches.Add({W, G});
			}
		}
	};
	for (UUserWidget* U : Live)
	{
		if (!U)
		{
			continue;
		}
		if (U->GetName() == WidgetName)
		{
			Consider(U);
		}
		if (U->WidgetTree)
		{
			Consider(U->WidgetTree->FindWidget(FName(*WidgetName)));
		}
	}
	if (Matches.Num() == 0)
	{
		return PointerError(FString::Printf(TEXT("no visible live widget named '%s'"), *WidgetName));
	}
	if (Matches.Num() > 1)
	{
		FString Names;
		for (const TPair<UWidget*, FGeometry>& M : Matches)
		{
			Names += (Names.IsEmpty() ? TEXT("") : TEXT(", ")) + M.Key->GetPathName();
		}
		return PointerError(FString::Printf(TEXT("%d visible widgets are named '%s': %s"), Matches.Num(), *WidgetName, *Names));
	}
	TSharedPtr<SWidget> Target = Matches[0].Key->GetCachedWidget();
	if (!Target.IsValid() || !Target->IsEnabled())
	{
		return PointerError(FString::Printf(TEXT("'%s' is disabled"), *WidgetName));
	}
	const FGeometry& G = Matches[0].Value;
	const FVector2D Screen(G.LocalToAbsolute(FVector2D(G.GetLocalSize()) * 0.5));
	const FVector2D At = ScreenToViewport(Screen);
	// Refuse rather than click whatever covers it.
	if (!VirtualUser.IsValid())
	{
		VirtualUser = App.FindOrCreateVirtualUser(VirtualUserIndex);
	}
	const FWidgetPath Under = App.LocateWindowUnderMouse(Screen, App.GetInteractiveTopLevelWindows(), false, VirtualUser->GetUserIndex());
	if (!Under.IsValid() || !Under.ContainsWidget(Target.Get()))
	{
		const FString Top = Under.IsValid() ? Under.Widgets.Last().Widget->GetTypeAsString() : FString(TEXT("nothing"));
		return PointerError(FString::Printf(TEXT("'%s' is covered at its centre by %s"), *WidgetName, *Top));
	}
	FString Hit, Refusal;
	const bool bHandled = Click(At, Key, Hit, Refusal);
	if (!Refusal.IsEmpty())
	{
		return PointerError(Refusal);
	}
	return PointerResult(At, bHandled, Hit, Matches[0].Key->GetPathName());
}

// --- API 5: spawn into the game world --------------------------------------------------

AActor* UMCPControlSubsystem::SpawnInGame(TSubclassOf<AActor> ActorClass, FVector Location, FRotator Rotation)
{
	UWorld* World = GetWorld();
	if (!World || !ActorClass || ActorClass->HasAnyClassFlags(CLASS_Abstract))
	{
		return nullptr;
	}
	FActorSpawnParameters Params;
	Params.SpawnCollisionHandlingOverride = ESpawnActorCollisionHandlingMethod::AdjustIfPossibleButAlwaysSpawn;
	return World->SpawnActor<AActor>(ActorClass, Location, Rotation, Params);
}
