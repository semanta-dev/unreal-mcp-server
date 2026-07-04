// Copyright unreal-mcp-server. MIT.
#pragma once

#include "CoreMinimal.h"
#include "HAL/Runnable.h"
#include "HAL/ThreadSafeCounter64.h"
#include "Containers/SpscQueue.h"
#include "Containers/Ticker.h"
#include "Templates/Atomic.h"
#include "Interfaces/IPv4/IPv4Endpoint.h"

class FTcpListener;
class FSocket;
class FRunnableThread;
class FJsonObject;

/**
 * FMCPCockpitServer — the process-lifetime transport singleton (EDITOR_PLUGIN_PLAN.md
 * §2.7). It is a plain C++ singleton (NOT a UObject/subsystem) so it is EXCLUDED from
 * Live Coding reinstancing: a hot-reload of other modules leaves the socket, event
 * ring, and session_epoch intact (§2.5 self-decapitation). Owns:
 *   - an FTcpListener on 127.0.0.1:0 (ephemeral) + one connected Go peer socket,
 *   - a length-prefixed framed-JSON protocol matching internal/cockpit (Go side),
 *   - a bounded lossy event ring (drop-oldest) with a monotonic seq (§6.2),
 *   - a bounded rpc queue drained on the game thread, exactly one op at a time (§5.6),
 *   - session_epoch (minted once per boot) + token + the assigned port (§2.5).
 *
 * The op-body dispatch (ExecPythonCommandEx) is wired in Phase B1; in A0 the queue +
 * serialization + QUEUE_FULL + the framed transport + event push + replay are the
 * deliverable, with a pluggable dispatcher.
 */
class MCPCORE_API FMCPCockpitServer
{
public:
	/** The process-lifetime singleton. Get() lazily constructs; it never dies with a
	 *  module reinstance. */
	static FMCPCockpitServer& Get();

	/** Boot the listener (idempotent). Guard on GEditor at the call site. Returns false
	 *  if the socket could not be created. */
	bool Start();
	/** Tear down the listener + peer (idempotent). */
	void Stop();

	bool IsRunning() const { return bRunning; }
	int32 GetPort() const { return Port; }
	const FString& GetSessionEpoch() const { return SessionEpoch; }
	const FString& GetToken() const { return Token; }
	static int32 GetProtocolVersion();

	/** Emit an observation to the Go peer (thread-safe). Assigns the next seq, pushes to
	 *  the ring (drop-oldest if full), and sends if a peer is connected. */
	void EmitEvent(const FString& EventType, const FString& PayloadJson);
	/** Emit an op result to the Go peer (thread-safe). Called from the Python emit sink
	 *  (§5.1) or the reconciliation path. */
	void EmitResult(const FString& OpId, const FString& ResultEnvelopeJson);
	/** Emit long-op progress (thread-safe). */
	void EmitProgress(const FString& OpId, const FString& PayloadJson);

	/** Called by the rx thread for each inbound hello/rpc/control frame (public so the
	 *  FRunnable can reach it). Returns false to stop reading (reject); teardown of a
	 *  rejected peer is deferred to the game thread (never self-joins the rx thread). */
	bool HandleInboundFrame(const FString& Json);

	/** A queued rpc waiting for the game thread. */
	struct FPendingRpc
	{
		FString OpId;
		FString Op;
		FString ArgsJson;
		FString Intent;
		FString TaskId;
	};

	/** The dispatcher runs one rpc on the game thread and MUST guarantee exactly one
	 *  terminal EmitResult(OpId,...) per call (reconciliation is layered in B1). */
	using FDispatcher = TFunction<void(const FPendingRpc&)>;
	void SetDispatcher(FDispatcher InDispatcher) { Dispatcher = MoveTemp(InDispatcher); }

	/** Drop-oldest ring depth + queue depth (tunable via UMCPSettings later). */
	static constexpr int32 EventRingCapacity = 4096;
	static constexpr int32 RpcQueueDepth = 64;

private:
	FMCPCockpitServer() = default;
	~FMCPCockpitServer();
	FMCPCockpitServer(const FMCPCockpitServer&) = delete;

	// --- listener / peer ---
	// OnConnectionAccepted runs on the FTcpListener thread and only HANDS OFF the new
	// socket via PendingPeer; the game thread does ALL install/teardown so peer lifecycle
	// is single-threaded (no listener-vs-game-thread race on the rx thread).
	bool OnConnectionAccepted(FSocket* InSocket, const FIPv4Endpoint& Endpoint);
	void InstallPeer(FSocket* NewSocket);   // GAME THREAD ONLY: teardown old, wire the new peer + rx thread
	void ClosePeer();                        // GAME THREAD ONLY (or Stop after the ticker+listener are gone)
	bool SendFrame(const TSharedRef<class FJsonObject>& Frame); // serialized send to the peer
	void SendErrorResult(const FString& OpId, const FString& Code, const FString& Error);

	// --- game-thread rpc pump ---
	bool GameThreadTick(float Dt);  // FTSTicker: pop one rpc, dispatch (exactly one at a time)

	// --- ring ---
	void PushToRing(uint64 Seq, const FString& FrameJson);
	void ServeReplayFrom(uint64 FromSeq);

	FTcpListener* Listener = nullptr;
	FSocket* PeerSocket = nullptr;              // the single connected Go peer (game-thread owned)
	FRunnableThread* RxThread = nullptr;         // game-thread owned
	class FMCPRxRunnable* RxRunnable = nullptr;  // game-thread owned
	TAtomic<FSocket*> PendingPeer{ nullptr };    // listener thread → game thread hand-off slot

	FCriticalSection PeerCS;                     // guards PeerSocket read/null + send serialization
	FCriticalSection RingCS;                     // guards the event ring

	int32 Port = 0;
	FString SessionEpoch;                        // minted once in Start (FGuid)
	FString Token;
	FString ManifestDigest;                      // empty in A0; real capability digest in B2
	FThreadSafeCounter64 SeqCounter;             // monotonic event/progress seq

	TAtomic<bool> bRunning{ false };
	TAtomic<bool> bStopRequested{ false };       // set by a `stop` control frame (off-thread)
	TAtomic<bool> bRpcExecuting{ false };        // exactly-one-executing guard (§5.6)
	TAtomic<bool> bPeerRejected{ false };        // rx thread flags a bad-token peer; game thread ClosePeers it

	// bounded rpc queue (SPSC: rx thread produces, game thread consumes)
	TSpscQueue<FPendingRpc> RpcQueue;
	TAtomic<int32> RpcQueueLen{ 0 };

	// bounded lossy event ring
	struct FRingEntry { uint64 Seq; FString Json; };
	TArray<FRingEntry> Ring;                     // circular; RingCS-guarded
	int32 RingHead = 0;
	uint64 RingFloor = 0;                        // lowest seq still in the ring
	uint64 DroppedSinceEmit = 0;                 // ring drop-oldest counter (§6.2)

	FDispatcher Dispatcher;
	FTSTicker::FDelegateHandle TickHandle;
};
