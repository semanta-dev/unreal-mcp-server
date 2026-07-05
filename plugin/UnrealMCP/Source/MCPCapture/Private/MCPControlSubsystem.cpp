// Copyright unreal-mcp-server. MIT.
#include "MCPControlSubsystem.h"

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
