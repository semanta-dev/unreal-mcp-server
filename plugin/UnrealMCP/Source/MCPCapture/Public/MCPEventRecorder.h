// Copyright unreal-mcp-server. MIT.
//
// UMCPEventRecorder (plugin API 8, remediation R5.1) records the gameplay events the
// engine itself can see — damage taken (any / point), actors spawned and destroyed —
// stamped with world time into a bounded ring buffer that the companion drains by
// cursor. C++ on purpose: the hooks are per-actor delegates fired on the game thread
// mid-frame, where Python must not run.
//
// Bindings: every actor already in the world when recording starts (placed actors,
// the player, the core...) and every actor spawned after (world OnActorSpawned). All
// are removed on StopRecording and when the GameInstance ends (PIE end) — nothing of
// this object is left bound to an actor that outlives it.
#pragma once

#include "CoreMinimal.h"
#include "Subsystems/GameInstanceSubsystem.h"
#include "MCPEventRecorder.generated.h"

class AActor;
class AController;
class UDamageType;
class UPrimitiveComponent;
class FJsonObject;

UCLASS()
class MCPCAPTURE_API UMCPEventRecorder : public UGameInstanceSubsystem
{
	GENERATED_BODY()

public:
	/** Resolve this subsystem from a world context (the companion calls .get(world)). */
	UFUNCTION(BlueprintCallable, Category = "MCP|Events", meta = (WorldContext = "WorldContext"))
	static UMCPEventRecorder* Get(const UObject* WorldContext);

	/** Start recording (a running recording is restarted: its events are dropped).
	 *  Capacity: ring size (256..1,000,000; default 65,536). JSON {ok, bound, t, generation,
	 *  error?} — generation counts StartRecording calls: a drain under another generation
	 *  is a different recording. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Events")
	FString StartRecording(int32 Capacity);

	/** Stop and unbind everything. JSON {ok, unbound, still_bound, recorded, dropped}. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Events")
	FString StopRecording();

	/** The events after Cursor (a seq; 0 = from the start), oldest first, at most MaxEvents.
	 *  JSON {events: [{seq, t, kind, actor?, target?, by_player, data?}], next_cursor, gap,
	 *  dropped, more, recording, bound, t, generation, world_lost?, lost_t?}. gap/dropped:
	 *  events the ring overwrote before they were drained (or a cursor from another
	 *  recording). world_lost: the recorded world was torn down (map travel, a restart);
	 *  the recording stopped there — nothing after lost_t was recorded. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Events")
	FString DrainEventsJson(int64 Cursor, int32 MaxEvents);

	/** Actors in the world that still have one of this recorder's handlers bound (0
	 *  after StopRecording — the check behind "no bindings remain"). */
	UFUNCTION(BlueprintCallable, Category = "MCP|Events")
	int32 CountBoundActors() const;

	UFUNCTION(BlueprintCallable, Category = "MCP|Events")
	bool IsRecording() const { return bRecording; }

	virtual void Deinitialize() override;

private:
	struct FEvent
	{
		int64 Seq = 0;
		double T = 0.0;
		FName Kind;
		FString Actor;
		FString Target;
		bool bByPlayer = false;
		TSharedPtr<FJsonObject> Data;
	};

	void BindActor(AActor* Actor);
	void OnWorldCleanup(UWorld* World, bool bSessionEnded, bool bCleanupResources);
	void UnbindActor(AActor* Actor);
	int32 UnbindAll();
	void OnActorSpawned(AActor* Actor);
	void Record(FName Kind, const AActor* Actor, const AActor* Target, bool bByPlayer, TSharedPtr<FJsonObject> Data);

	UFUNCTION()
	void HandleAnyDamage(AActor* DamagedActor, float Damage, const UDamageType* DamageType, AController* InstigatedBy, AActor* DamageCauser);

	UFUNCTION()
	void HandlePointDamage(AActor* DamagedActor, float Damage, AController* InstigatedBy, FVector HitLocation,
		UPrimitiveComponent* HitComponent, FName BoneName, FVector ShotFromDirection, const UDamageType* DamageType, AActor* DamageCauser);

	UFUNCTION()
	void HandleDestroyed(AActor* DestroyedActor);

	bool bRecording = false;
	TWeakObjectPtr<UWorld> RecordingWorld;
	FDelegateHandle SpawnHandle;
	FDelegateHandle CleanupHandle;
	int32 Generation = 0;
	bool bWorldLost = false;
	double LostT = 0.0;
	double LastT = 0.0;
	TArray<TWeakObjectPtr<AActor>> Bound;
	TArray<FEvent> Ring;
	int32 Capacity = 0;
	int32 Head = 0;     // index of the oldest kept event
	int32 Count = 0;
	int64 NextSeq = 1;  // seq of the next event
	int64 Dropped = 0;  // overwritten before a drain read them
	int64 DrainedTo = 0;
};
