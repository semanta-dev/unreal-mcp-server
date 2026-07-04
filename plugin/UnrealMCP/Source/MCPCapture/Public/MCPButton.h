// Copyright unreal-mcp-server. MIT.
#pragma once

#include "CoreMinimal.h"
#include "Components/Button.h"
#include "MCPButton.generated.h"

/**
 * UMCPButton — a UButton that carries a persisted FName Command (HUD_TOOLING_PLAN.md
 * §3.0.1, fix #3). UButton::OnClicked is a ZERO-argument multicast delegate, so a
 * single shared handler bound to N buttons cannot tell which fired. UMCPButton solves
 * that by self-binding in RebuildWidget and re-broadcasting through a param-carrying
 * delegate that names the Command — the owning UMCPHUDWidget consumes it and calls
 * RunNamedCommand(Command). No serialized delegate, no graph node, unambiguous dispatch.
 */
UCLASS()
class MCPCAPTURE_API UMCPButton : public UButton
{
	GENERATED_BODY()

public:
	/** The named command this button fires (Resume/Quit/OpenPanel/…). Set by widget_bind_event. */
	UPROPERTY(EditAnywhere, BlueprintReadWrite, Category = "MCP")
	FName Command;

	DECLARE_DYNAMIC_MULTICAST_DELEGATE_OneParam(FMCPCommandClicked, FName, Command);

	/** Fired on click, carrying Command. UMCPHUDWidget binds this to RunNamedCommand. */
	UPROPERTY(BlueprintAssignable, Category = "MCP")
	FMCPCommandClicked OnCommand;

protected:
	virtual TSharedRef<SWidget> RebuildWidget() override;

	UFUNCTION()
	void HandleMCPClicked();
};
