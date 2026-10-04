// Copyright unreal-mcp-server. MIT.
#pragma once

#include "CoreMinimal.h"
#include "Kismet/BlueprintFunctionLibrary.h"
#include "MCPCoreLibrary.generated.h"

class USubsystem;

/**
 * Editor helpers the companion calls where UE 5.7's Python surface stops
 * (docs/plans/REMEDIATION_SPIKES.md): the plugin API version handshake, function-flag
 * reads, the undo buffer's titles and a title-checked undo, and game-world subsystem
 * lookup. Every function is static and BlueprintCallable so Python reaches it as
 * unreal.MCPCoreLibrary.<snake_case>.
 */
UCLASS()
class MCPCORE_API UMCPCoreLibrary : public UBlueprintFunctionLibrary
{
	GENERATED_BODY()

public:
	/** The plugin API version the server's ops declare in Needs (plugin>=N). Bumped by
	 *  every change to what the plugin offers the companion (REMEDIATION_PLAN.md §8). */
	UFUNCTION(BlueprintCallable, Category = "MCP|Core")
	static int32 GetPluginApiVersion();

	/** True when Class has a function FunctionName that is BlueprintPure or const —
	 *  the only functions a read-only predicate may call. False when it has none. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Core")
	static bool IsPureOrConst(UClass* Class, FName FunctionName);

	/** Plugin API 8: seed the engine's global random streams (FMath::Rand / FRand /
	 *  SRand) — what gameplay code that does not keep its own stream draws from. A seeded
	 *  playtest batch (R5.5) calls it at the start of each run; frame timing still varies. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Core")
	static void SeedRandomStreams(int32 Seed);

	/** Plugin API 9: why Python's set_editor_property(Name) on Object would be refused —
	 *  the same check (PropertyAccessUtil::CanSetPropertyValue with the editor's read-only
	 *  flags) — or "" when it would be allowed. Name: the reflected name, or its Python
	 *  spelling (case and underscores ignored) when that is unambiguous. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Core")
	static FString WhyNotSettable(UObject* Object, const FString& Name);

	/** Plugin API 9: the object type (1..32, ObjectTypeQuery N) of the project's collision
	 *  object channel named ChannelName (Project Settings > Collision), or 0 when no
	 *  object channel has that name. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Core")
	static int32 ObjectTypeByChannelName(const FString& ChannelName);

	/** The title of the transaction Ctrl+Z would undo next ("" when there is none). */
	UFUNCTION(BlueprintCallable, Category = "MCP|Core")
	static FString PeekUndoTitle();

	/** The title of the transaction Ctrl+Y would redo next ("" when there is none). */
	UFUNCTION(BlueprintCallable, Category = "MCP|Core")
	static FString PeekRedoTitle();

	/** Undo (or redo) the next transaction ONLY if its title starts with Prefix: the
	 *  check and the undo run in one game-thread call, so no edit can land between them.
	 *  Returns JSON {"ok": bool, "title": "...", "reason"?: "empty|title_mismatch|pie|transaction_active|failed"}. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Core")
	static FString UndoIfTitled(const FString& Prefix);

	/** Redo counterpart of UndoIfTitled. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Core")
	static FString RedoIfTitled(const FString& Prefix);

	/** The World, GameInstance or LocalPlayer (first player) subsystem of class Class in
	 *  WorldContext's world — Python has no accessor for these. Null when absent. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Core")
	static USubsystem* FindGameSubsystem(UObject* WorldContext, UClass* Class);
};
