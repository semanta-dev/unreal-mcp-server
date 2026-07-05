// Copyright unreal-mcp-server. MIT.
//
// UMCPCaptureSubsystem films the LIVE game world (possessed PIE / standalone)
// with a SceneCapture2D spawned INTO that world, exporting one PNG per interval
// to disk with a manifest. This is the capability the Python bridge cannot reach:
// unreal.EditorActorSubsystem spawns a SceneCapture2D only into the EDITOR world
// (NoneType.capture_component2d in PIE), and HighResShot won't flush a frame when
// the editor is backgrounded/headless. A SceneCapture2D in the game world renders
// regardless of viewport focus, so this captures the real gameplay sequence.
//
// Driven by the MCP server via reflection: the bridge's capture_start (source=
// game_scene) / capture_stop / capture_poll call StartCapture/StopCapture/
// PollCapture on this subsystem, resolved from the PIE GameInstance. The exported
// frames + manifest.json feed the server's existing montage/timeline pipeline.
#pragma once

#include "CoreMinimal.h"
#include "Subsystems/GameInstanceSubsystem.h"
#include "Engine/TimerHandle.h" // FTimerHandle by-value member
#include "MCPCaptureSubsystem.generated.h"

class ASceneCapture2D;
class USceneCaptureComponent2D;
class UTextureRenderTarget2D;

UENUM(BlueprintType)
enum class EMCPCaptureCamera : uint8
{
	// Follow the local player's camera-manager POV (the live gameplay view).
	Player  UMETA(DisplayName = "Player POV"),
	// A fixed pose (FixedLocation/FixedRotation/FixedFOV).
	Fixed   UMETA(DisplayName = "Fixed pose"),
	// Ride an actor's transform (matched by label in-editor, else by name).
	Actor   UMETA(DisplayName = "Follow actor"),
};

UCLASS()
class MCPCAPTURE_API UMCPCaptureSubsystem : public UGameInstanceSubsystem
{
	GENERATED_BODY()

public:
	/**
	 * Resolve this subsystem from a world context (the MCP bridge calls
	 * unreal.MCPCaptureSubsystem.get(world) — Python cannot call the C++ template
	 * UGameInstance::GetSubsystem<T>() directly).
	 */
	UFUNCTION(BlueprintCallable, Category = "MCP|Capture", meta = (WorldContext = "WorldContext"))
	static UMCPCaptureSubsystem* Get(const UObject* WorldContext);

	/**
	 * Begin filming the game world to OutDir at IntervalSeconds, exporting
	 * <FilePrefix>fNNNNN.png at Width x Height until MaxFrames / MaxSeconds.
	 * Returns the session id (echoing Session), or empty on failure.
	 */
	UFUNCTION(BlueprintCallable, Category = "MCP|Capture")
	FString StartCapture(const FString& Session, const FString& OutDir, const FString& FilePrefix,
	                     int32 Width, int32 Height, float IntervalSeconds, int32 MaxFrames, float MaxSeconds,
	                     EMCPCaptureCamera Camera, FVector FixedLocation, FRotator FixedRotation,
	                     float FixedFOV, const FString& ActorLabel, bool bIncludeUI);

	/** Stop and return the manifest JSON ({dir,frame_count,cell_width,cell_height,frames:[...],stop_reason}). */
	UFUNCTION(BlueprintCallable, Category = "MCP|Capture")
	FString StopCapture();

	/** One-shot status JSON ({running,frames_captured,last_t_world,dir}). */
	UFUNCTION(BlueprintCallable, Category = "MCP|Capture")
	FString PollCapture() const;

	UFUNCTION(BlueprintCallable, Category = "MCP|Capture")
	bool IsCapturing() const { return bRunning; }

	// --- Audio submix tap (AGENTIC_GAMEDEV_PLAN.md §6.3, RC9) ---------------------
	// Registers an ISubmixBufferListener on the main output submix and reduces the
	// PCM stream to an RMS/peak envelope so a build can be certified non-silent (the
	// audio half of "you get what you measure"). StopAudioCapture writes the envelope
	// as JSONL ({t,rms,peak} per submix buffer) the server's audio_audit consumes.

	/** Begin tapping the main submix. Returns the session id, or empty on failure. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Audio")
	FString StartAudioCapture(const FString& InSession);

	/** Stop the tap and write <Session>_audio.jsonl to InOutDir. Returns a JSON summary
	 *  ({path,points,max_rms,duration}) or empty if not running. */
	UFUNCTION(BlueprintCallable, Category = "MCP|Audio")
	FString StopAudioCapture(const FString& InOutDir);

	UFUNCTION(BlueprintCallable, Category = "MCP|Audio")
	bool IsAudioCapturing() const { return AudioListener.IsValid(); }

	// USubsystem
	virtual void Deinitialize() override;

private:
	void OnCaptureTick();
	void CaptureFrame();
	bool PositionCapture();
	// Grab the on-screen game viewport (scene + Slate/UMG HUD composited) via
	// FSlateApplication and save it as PNG. Needs a rendering window (the visible
	// PIE viewport); the 3D-only SceneCapture path works headless, this does not.
	bool CaptureViewportUI(const FString& AbsPath);
	void SelfStop(const FString& Reason);
	void Teardown();
	void FlushManifest() const;
	FString BuildManifestJson() const;

	UPROPERTY(Transient)
	TObjectPtr<ASceneCapture2D> CaptureActor = nullptr;

	UPROPERTY(Transient)
	TObjectPtr<UTextureRenderTarget2D> RenderTarget = nullptr;

	// Weak ref to the component owned by CaptureActor (not separately GC-rooted).
	USceneCaptureComponent2D* CaptureComp = nullptr;

	FTimerHandle CaptureTimer;

	// Audio submix tap: the listener is held alive here (the audio device also holds a
	// shared ref while registered). AudioSession names the output JSONL.
	TSharedPtr<class FMCPSubmixListener, ESPMode::ThreadSafe> AudioListener;
	FString AudioSession;
	void StopAudioTapInternal();

	bool bRunning = false;
	bool bIncludeUI = false;
	FString Session, OutDir, FilePrefix, ActorLabel, StopReason;
	int32 Width = 0, Height = 0, MaxFrames = 0;
	float IntervalSeconds = 0.f, MaxSeconds = 0.f, StartWorldTime = 0.f, FixedFOV = 90.f;
	EMCPCaptureCamera Camera = EMCPCaptureCamera::Player;
	FVector FixedLocation = FVector::ZeroVector;
	FRotator FixedRotation = FRotator::ZeroRotator;

	struct FMCPFrame
	{
		int32 Index = 0;
		FString File;
		float TWall = 0.f;
		float TWorld = 0.f;
	};
	TArray<FMCPFrame> Frames;
};
