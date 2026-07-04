// Copyright unreal-mcp-server. MIT.
#pragma once

#include "CoreMinimal.h"
#include "EditorSubsystem.h"
#include "MCPCockpitBridge.generated.h"

/**
 * UMCPCockpitBridge — the Python/Blueprint-reachable façade over the process-lifetime
 * FMCPCockpitServer singleton (EDITOR_PLUGIN_PLAN.md §5.1). Reached from the bridge as
 * unreal.get_editor_subsystem(unreal.MCPCockpitBridge). It is a THIN forwarder: a
 * Live-Coding reinstance of this subsystem does not lose the socket/ring/epoch, which
 * live in the singleton. Boots the server on Initialize (guarded on GEditor) and tears
 * it down on Deinitialize.
 *
 * - CockpitInfo() is the native-presence probe: if MCPCore is loaded, it returns
 *   {cockpit_port, session_epoch, token, protocol_version}; the Python one-liner
 *   returns {cockpit:"not_present"} when this subsystem is absent (§2.5).
 * - EmitResult/EmitEvent/EmitProgress are the emit sink the Python op bodies route to
 *   when a native sink is active for the current dispatch (§5.1).
 */
UCLASS()
class MCPCORE_API UMCPCockpitBridge : public UEditorSubsystem
{
	GENERATED_BODY()

public:
	virtual void Initialize(FSubsystemCollectionBase& Collection) override;
	virtual void Deinitialize() override;

	/** JSON: {cockpit_port, session_epoch, token, protocol_version}. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Cockpit")
	FString CockpitInfo() const;

	/** Route an op result to the connected Go peer (the native emit sink). */
	UFUNCTION(BlueprintCallable, Category = "MCP|Cockpit")
	void EmitResult(const FString& OpId, const FString& ResultEnvelopeJson);

	/** Route a pushed observation to the Go peer. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Cockpit")
	void EmitEvent(const FString& EventType, const FString& PayloadJson);

	/** Route long-op progress to the Go peer. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Cockpit")
	void EmitProgress(const FString& OpId, const FString& PayloadJson);
};
