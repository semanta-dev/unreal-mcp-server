// Copyright unreal-mcp-server. MIT.
#include "MCPEventRecorder.h"

#include "Dom/JsonObject.h"
#include "Dom/JsonValue.h"
#include "Serialization/JsonSerializer.h"
#include "Serialization/JsonWriter.h"
#include "Engine/Engine.h"
#include "Engine/GameInstance.h"
#include "Engine/World.h"
#include "EngineUtils.h" // TActorIterator
#include "GameFramework/Actor.h"
#include "GameFramework/Controller.h"
#include "GameFramework/DamageType.h"
#include "GameFramework/Pawn.h"
#include "Components/PrimitiveComponent.h"

namespace
{
	// Object names, as game journals report them (labels are an editor-only, non-unique
	// display name): the engine and journal events of one actor must join.
	FString MCPActorName(const AActor* A)
	{
		return A ? A->GetName() : FString();
	}

	void MCPAddLabels(const TSharedPtr<FJsonObject>& Data, const AActor* Actor, const AActor* Target)
	{
#if WITH_EDITOR
		if (Actor && Actor->GetActorLabel() != Actor->GetName())
		{
			Data->SetStringField(TEXT("actor_label"), Actor->GetActorLabel());
		}
		if (Target && Target->GetActorLabel() != Target->GetName())
		{
			Data->SetStringField(TEXT("target_label"), Target->GetActorLabel());
		}
#endif
	}

	FString MCPToJson(const TSharedRef<FJsonObject>& Obj)
	{
		FString S;
		TSharedRef<TJsonWriter<>> W = TJsonWriterFactory<>::Create(&S);
		FJsonSerializer::Serialize(Obj, W);
		return S;
	}

	TArray<TSharedPtr<FJsonValue>> MCPVec(const FVector& V)
	{
		return {MakeShared<FJsonValueNumber>(V.X), MakeShared<FJsonValueNumber>(V.Y), MakeShared<FJsonValueNumber>(V.Z)};
	}
}

UMCPEventRecorder* UMCPEventRecorder::Get(const UObject* WorldContext)
{
	UWorld* World = GEngine ? GEngine->GetWorldFromContextObject(WorldContext, EGetWorldErrorMode::ReturnNull) : nullptr;
	UGameInstance* GI = World ? World->GetGameInstance() : nullptr;
	return GI ? GI->GetSubsystem<UMCPEventRecorder>() : nullptr;
}

FString UMCPEventRecorder::StartRecording(int32 InCapacity)
{
	TSharedRef<FJsonObject> Out = MakeShared<FJsonObject>();
	UWorld* World = GetGameInstance() ? GetGameInstance()->GetWorld() : nullptr;
	if (!World || !World->IsGameWorld())
	{
		Out->SetBoolField(TEXT("ok"), false);
		Out->SetStringField(TEXT("error"), TEXT("no game world (start PIE first)"));
		return MCPToJson(Out);
	}
	if (bRecording)
	{
		StopRecording();
	}
	Capacity = FMath::Clamp(InCapacity > 0 ? InCapacity : 65536, 256, 1000000);
	Ring.SetNum(Capacity);
	Head = Count = 0;
	NextSeq = 1;
	Dropped = DrainedTo = 0;
	RecordingWorld = World;
	bRecording = true;
	for (TActorIterator<AActor> It(World); It; ++It)
	{
		BindActor(*It); // pre-existing actors: placed nests, the core, the player
	}
	SpawnHandle = World->AddOnActorSpawnedHandler(FOnActorSpawned::FDelegate::CreateUObject(this, &UMCPEventRecorder::OnActorSpawned));
	Out->SetBoolField(TEXT("ok"), true);
	Out->SetNumberField(TEXT("bound"), Bound.Num());
	Out->SetNumberField(TEXT("capacity"), Capacity);
	Out->SetNumberField(TEXT("t"), World->GetTimeSeconds());
	return MCPToJson(Out);
}

FString UMCPEventRecorder::StopRecording()
{
	TSharedRef<FJsonObject> Out = MakeShared<FJsonObject>();
	const bool bWas = bRecording;
	bRecording = false;
	if (UWorld* World = RecordingWorld.Get())
	{
		World->RemoveOnActorSpawnedHandler(SpawnHandle);
	}
	SpawnHandle.Reset();
	const int32 Unbound = UnbindAll();
	Out->SetBoolField(TEXT("ok"), true);
	Out->SetBoolField(TEXT("was_recording"), bWas);
	Out->SetNumberField(TEXT("unbound"), Unbound);
	Out->SetNumberField(TEXT("still_bound"), CountBoundActors());
	Out->SetNumberField(TEXT("recorded"), (double)(NextSeq - 1));
	Out->SetNumberField(TEXT("dropped"), (double)Dropped);
	return MCPToJson(Out);
}

void UMCPEventRecorder::Deinitialize()
{
	if (bRecording || Bound.Num() > 0)
	{
		const int32 Was = Bound.Num();
		StopRecording();
		UE_LOG(LogTemp, Display, TEXT("MCPEventRecorder: game instance ended while recording: unbound %d actors (%d still bound)"),
			Was, CountBoundActors());
	}
	Super::Deinitialize();
}

void UMCPEventRecorder::BindActor(AActor* Actor)
{
	if (!Actor || Actor->IsActorBeingDestroyed())
	{
		return;
	}
	Actor->OnTakeAnyDamage.AddUniqueDynamic(this, &UMCPEventRecorder::HandleAnyDamage);
	Actor->OnTakePointDamage.AddUniqueDynamic(this, &UMCPEventRecorder::HandlePointDamage);
	Actor->OnDestroyed.AddUniqueDynamic(this, &UMCPEventRecorder::HandleDestroyed);
	Bound.Add(Actor);
}

void UMCPEventRecorder::UnbindActor(AActor* Actor)
{
	if (!Actor)
	{
		return;
	}
	Actor->OnTakeAnyDamage.RemoveDynamic(this, &UMCPEventRecorder::HandleAnyDamage);
	Actor->OnTakePointDamage.RemoveDynamic(this, &UMCPEventRecorder::HandlePointDamage);
	Actor->OnDestroyed.RemoveDynamic(this, &UMCPEventRecorder::HandleDestroyed);
}

int32 UMCPEventRecorder::UnbindAll()
{
	int32 N = 0;
	for (const TWeakObjectPtr<AActor>& W : Bound)
	{
		if (AActor* A = W.Get())
		{
			UnbindActor(A);
			++N;
		}
	}
	Bound.Reset();
	return N;
}

int32 UMCPEventRecorder::CountBoundActors() const
{
	UWorld* World = RecordingWorld.Get();
	if (!World)
	{
		World = GetGameInstance() ? GetGameInstance()->GetWorld() : nullptr;
	}
	if (!World)
	{
		return 0;
	}
	UMCPEventRecorder* Self = const_cast<UMCPEventRecorder*>(this);
	int32 N = 0;
	for (TActorIterator<AActor> It(World); It; ++It)
	{
		AActor* A = *It;
		if (A->OnTakeAnyDamage.IsAlreadyBound(Self, &UMCPEventRecorder::HandleAnyDamage)
			|| A->OnTakePointDamage.IsAlreadyBound(Self, &UMCPEventRecorder::HandlePointDamage)
			|| A->OnDestroyed.IsAlreadyBound(Self, &UMCPEventRecorder::HandleDestroyed))
		{
			++N;
		}
	}
	return N;
}

void UMCPEventRecorder::OnActorSpawned(AActor* Actor)
{
	if (!bRecording || !Actor)
	{
		return;
	}
	BindActor(Actor);
	TSharedPtr<FJsonObject> Data = MakeShared<FJsonObject>();
	Data->SetStringField(TEXT("class"), Actor->GetClass()->GetName());
	Record(TEXT("spawned"), Actor, nullptr, false, Data);
}

void UMCPEventRecorder::Record(FName Kind, const AActor* Actor, const AActor* Target, bool bByPlayer, TSharedPtr<FJsonObject> Data)
{
	if (!bRecording || Capacity <= 0)
	{
		return;
	}
	FEvent E;
	E.Seq = NextSeq++;
	E.T = RecordingWorld.IsValid() ? RecordingWorld->GetTimeSeconds() : 0.0;
	E.Kind = Kind;
	E.Actor = MCPActorName(Actor);
	E.Target = MCPActorName(Target);
	E.bByPlayer = bByPlayer;
	if (Data.IsValid())
	{
		MCPAddLabels(Data, Actor, Target);
	}
	E.Data = Data;
	if (Count < Capacity)
	{
		Ring[(Head + Count) % Capacity] = MoveTemp(E);
		++Count;
	}
	else
	{
		// Full: the oldest goes. It is a loss only if no drain read it yet.
		if (Ring[Head].Seq > DrainedTo)
		{
			++Dropped;
		}
		Ring[Head] = MoveTemp(E);
		Head = (Head + 1) % Capacity;
	}
}

void UMCPEventRecorder::HandleAnyDamage(AActor* DamagedActor, float Damage, const UDamageType* DamageType, AController* InstigatedBy, AActor* DamageCauser)
{
	const AActor* By = InstigatedBy && InstigatedBy->GetPawn() ? InstigatedBy->GetPawn() : DamageCauser;
	TSharedPtr<FJsonObject> Data = MakeShared<FJsonObject>();
	Data->SetNumberField(TEXT("damage"), Damage);
	Data->SetStringField(TEXT("damage_type"), DamageType ? DamageType->GetClass()->GetName() : FString());
	Data->SetStringField(TEXT("causer"), MCPActorName(DamageCauser));
	Record(TEXT("damage"), By, DamagedActor, InstigatedBy && InstigatedBy->IsPlayerController(), Data);
}

void UMCPEventRecorder::HandlePointDamage(AActor* DamagedActor, float Damage, AController* InstigatedBy, FVector HitLocation,
	UPrimitiveComponent* HitComponent, FName BoneName, FVector ShotFromDirection, const UDamageType* DamageType, AActor* DamageCauser)
{
	const AActor* By = InstigatedBy && InstigatedBy->GetPawn() ? InstigatedBy->GetPawn() : DamageCauser;
	TSharedPtr<FJsonObject> Data = MakeShared<FJsonObject>();
	Data->SetNumberField(TEXT("damage"), Damage);
	Data->SetArrayField(TEXT("location"), MCPVec(HitLocation));
	Data->SetArrayField(TEXT("from_direction"), MCPVec(ShotFromDirection));
	if (BoneName != NAME_None)
	{
		Data->SetStringField(TEXT("bone"), BoneName.ToString());
	}
	Data->SetStringField(TEXT("causer"), MCPActorName(DamageCauser));
	Record(TEXT("point_damage"), By, DamagedActor, InstigatedBy && InstigatedBy->IsPlayerController(), Data);
}

void UMCPEventRecorder::HandleDestroyed(AActor* DestroyedActor)
{
	TSharedPtr<FJsonObject> Data = MakeShared<FJsonObject>();
	Data->SetStringField(TEXT("class"), DestroyedActor ? DestroyedActor->GetClass()->GetName() : FString());
	Record(TEXT("destroyed"), DestroyedActor, nullptr, false, Data);
	Bound.RemoveAllSwap([DestroyedActor](const TWeakObjectPtr<AActor>& W) { return !W.IsValid() || W.Get() == DestroyedActor; });
}

FString UMCPEventRecorder::DrainEventsJson(int64 Cursor, int32 MaxEvents)
{
	TSharedRef<FJsonObject> Out = MakeShared<FJsonObject>();
	const int64 Oldest = NextSeq - Count; // seq of the oldest kept event (== NextSeq when empty)
	const int64 From = FMath::Max(Cursor + 1, Oldest);
	const int64 Lost = FMath::Max<int64>(0, Oldest - (Cursor + 1));
	const int32 Max = FMath::Clamp(MaxEvents > 0 ? MaxEvents : 10000, 1, 100000);
	TArray<TSharedPtr<FJsonValue>> Events;
	int64 Seq = From;
	for (; Seq < NextSeq && Events.Num() < Max; ++Seq)
	{
		const FEvent& E = Ring[(Head + (int32)(Seq - Oldest)) % Capacity];
		TSharedRef<FJsonObject> J = MakeShared<FJsonObject>();
		J->SetNumberField(TEXT("seq"), (double)E.Seq);
		J->SetNumberField(TEXT("t"), E.T);
		J->SetStringField(TEXT("kind"), E.Kind.ToString());
		if (!E.Actor.IsEmpty())
		{
			J->SetStringField(TEXT("actor"), E.Actor);
		}
		if (!E.Target.IsEmpty())
		{
			J->SetStringField(TEXT("target"), E.Target);
		}
		J->SetBoolField(TEXT("by_player"), E.bByPlayer);
		if (E.Data.IsValid())
		{
			J->SetObjectField(TEXT("data"), E.Data);
		}
		Events.Add(MakeShared<FJsonValueObject>(J));
	}
	const int64 Next = Seq - 1; // From >= Cursor + 1, so never behind the cursor
	DrainedTo = FMath::Max(DrainedTo, Next);
	Out->SetArrayField(TEXT("events"), Events);
	Out->SetNumberField(TEXT("next_cursor"), (double)Next);
	Out->SetBoolField(TEXT("gap"), Lost > 0);
	Out->SetNumberField(TEXT("dropped"), (double)Lost);
	Out->SetBoolField(TEXT("more"), Seq < NextSeq);
	Out->SetBoolField(TEXT("recording"), bRecording);
	Out->SetNumberField(TEXT("bound"), Bound.Num());
	Out->SetNumberField(TEXT("t"), RecordingWorld.IsValid() ? RecordingWorld->GetTimeSeconds() : 0.0);
	return MCPToJson(Out);
}
