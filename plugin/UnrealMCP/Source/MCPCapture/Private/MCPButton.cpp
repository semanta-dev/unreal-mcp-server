// Copyright unreal-mcp-server. MIT.
#include "MCPButton.h"

TSharedRef<SWidget> UMCPButton::RebuildWidget()
{
	TSharedRef<SWidget> Widget = Super::RebuildWidget();
	// Self-bind at runtime (not a serialized delegate) so every UMCPButton dispatches
	// its own Command unambiguously.
	if (!OnClicked.IsAlreadyBound(this, &UMCPButton::HandleMCPClicked))
	{
		OnClicked.AddDynamic(this, &UMCPButton::HandleMCPClicked);
	}
	return Widget;
}

void UMCPButton::HandleMCPClicked()
{
	OnCommand.Broadcast(Command);
}
