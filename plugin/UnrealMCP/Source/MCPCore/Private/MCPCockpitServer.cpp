// Copyright unreal-mcp-server. MIT.
#include "MCPCockpitServer.h"

#include "Common/TcpListener.h"
#include "Sockets.h"
#include "SocketSubsystem.h"
#include "Interfaces/IPv4/IPv4Endpoint.h"
#include "HAL/RunnableThread.h"
#include "Misc/ScopeLock.h"
#include "Serialization/JsonSerializer.h"
#include "Serialization/JsonWriter.h"
#include "Dom/JsonObject.h"
#include "Misc/Guid.h"
#include "Misc/EngineVersion.h"
#include "Misc/Paths.h"
#include "Misc/Base64.h"
#include "Misc/SecureHash.h"
#include "IPythonScriptPlugin.h"
#include "PythonScriptTypes.h"

static const int32 MCP_PROTOCOL_VERSION = 1;
static constexpr uint32 MCP_MAX_FRAME_BYTES = 64u << 20; // matches Go MaxFrameBytes

// ---------------------------------------------------------------------------
// Framing helpers: 4-byte big-endian length prefix + UTF-8 JSON body, matching the Go
// internal/cockpit codec exactly (encode + decode are symmetric UTF-8).
// ---------------------------------------------------------------------------

static FString MCPJsonObjectToString(const TSharedRef<FJsonObject>& Obj)
{
	FString Out;
	TSharedRef<TJsonWriter<>> Writer = TJsonWriterFactory<>::Create(&Out);
	FJsonSerializer::Serialize(Obj, Writer);
	return Out;
}

static TSharedPtr<FJsonObject> MCPJsonParse(const FString& Str)
{
	TSharedPtr<FJsonObject> Obj;
	TSharedRef<TJsonReader<>> Reader = TJsonReaderFactory<>::Create(Str);
	FJsonSerializer::Deserialize(Reader, Obj);
	return Obj;
}

// Serialize one frame to a length-prefixed byte buffer (4-byte BE length + UTF-8 body).
static void MCPBuildFrameBytes(const TSharedRef<FJsonObject>& Frame, TArray<uint8>& OutBuf)
{
	const FString Json = MCPJsonObjectToString(Frame);
	FTCHARToUTF8 Utf8(*Json);
	const int32 Len = Utf8.Length();
	OutBuf.SetNumUninitialized(4 + Len);
	OutBuf[0] = (Len >> 24) & 0xFF; OutBuf[1] = (Len >> 16) & 0xFF; OutBuf[2] = (Len >> 8) & 0xFF; OutBuf[3] = Len & 0xFF;
	FMemory::Memcpy(OutBuf.GetData() + 4, Utf8.Get(), Len);
}

// Loops on partial sends so a frame larger than one socket send buffer is never truncated
// on the wire (a large rpc_result must arrive whole or the Go ReadFrame desyncs).
static bool MCPSendAll(FSocket* Socket, const uint8* Data, int32 Num)
{
	int32 Total = 0;
	while (Total < Num)
	{
		int32 Sent = 0;
		if (!Socket->Send(Data + Total, Num - Total, Sent) || Sent <= 0)
		{
			return false;
		}
		Total += Sent;
	}
	return true;
}

// Rx thread: reads framed hello/rpc/control from the peer and hands them to the server.
// It NEVER closes/destroys the socket or joins itself — teardown is owned by the server
// on the listener/game thread (deadlock-free teardown). It only reads and hands off.
class FMCPRxRunnable : public FRunnable
{
public:
	FMCPRxRunnable(FMCPCockpitServer* InServer, FSocket* InSocket) : Server(InServer), Socket(InSocket) {}
	virtual bool Init() override { return true; }
	virtual void Stop() override { bStop = true; }
	virtual uint32 Run() override
	{
		while (!bStop)
		{
			uint8 Hdr[4];
			if (!RecvExact(Hdr, 4)) break;
			const uint32 Len = (uint32(Hdr[0]) << 24) | (uint32(Hdr[1]) << 16) | (uint32(Hdr[2]) << 8) | uint32(Hdr[3]);
			if (Len == 0 || Len > MCP_MAX_FRAME_BYTES) break;
			TArray<uint8> Body;
			Body.SetNumUninitialized(Len);
			if (!RecvExact(Body.GetData(), Len)) break;
			// Decode the body as UTF-8 (symmetric with the FTCHARToUTF8 encode on send).
			FString Json(FUTF8ToTCHAR(reinterpret_cast<const ANSICHAR*>(Body.GetData()), Len));
			if (!Server->HandleInboundFrame(Json)) break; // false = reject/stop reading
		}
		return 0;
	}

private:
	// Blocking-with-timeout read of exactly Num bytes; polls bStop between waits so a
	// close/Stop interrupts a parked read promptly (join is bounded).
	bool RecvExact(uint8* Dst, int32 Num)
	{
		int32 Total = 0;
		while (Total < Num)
		{
			if (bStop) return false;
			if (!Socket->Wait(ESocketWaitConditions::WaitForRead, FTimespan::FromMilliseconds(100)))
			{
				continue; // timeout: re-check bStop
			}
			int32 Read = 0;
			if (!Socket->Recv(Dst + Total, Num - Total, Read) || Read <= 0)
			{
				return false; // peer closed / error
			}
			Total += Read;
		}
		return true;
	}

	FMCPCockpitServer* Server;
	FSocket* Socket;
	TAtomic<bool> bStop{ false };
};

FMCPCockpitServer& FMCPCockpitServer::Get()
{
	static FMCPCockpitServer* Singleton = new FMCPCockpitServer(); // leaked → survives Live Coding reinstance
	return *Singleton;
}

FMCPCockpitServer::~FMCPCockpitServer() { Stop(); }

int32 FMCPCockpitServer::GetProtocolVersion() { return MCP_PROTOCOL_VERSION; }

bool FMCPCockpitServer::Start()
{
	if (bRunning) return true;
	SessionEpoch = FGuid::NewGuid().ToString(EGuidFormats::DigitsWithHyphens);
	Token = FGuid::NewGuid().ToString(EGuidFormats::Digits);
	{
		FScopeLock Lock(&RingCS);
		Ring.Reset();
		RingHead = 0;
		RingFloor = 0;
		DroppedSinceEmit = 0;
	}

	// Ephemeral loopback listener (127.0.0.1:0). FTcpListener creates+binds+listens on
	// its own thread; FRunnableThread::Create blocks until Init() bound the socket, so
	// GetSocket() immediately after is valid (verified 5.7).
	Listener = new FTcpListener(FIPv4Endpoint(FIPv4Address::InternalLoopback, 0));
	if (!Listener->GetSocket())
	{
		delete Listener;
		Listener = nullptr;
		return false;
	}
	Listener->OnConnectionAccepted().BindRaw(this, &FMCPCockpitServer::OnConnectionAccepted);

	TSharedRef<FInternetAddr> Addr = ISocketSubsystem::Get(PLATFORM_SOCKETSUBSYSTEM)->CreateInternetAddr();
	Listener->GetSocket()->GetAddress(*Addr);
	Port = Addr->GetPort();

	TickHandle = FTSTicker::GetCoreTicker().AddTicker(
		FTickerDelegate::CreateRaw(this, &FMCPCockpitServer::GameThreadTick), 0.0f);

	WireDefaultDispatcher(); // B1: run Tier-P op bodies in-process
	bRunning = true;
	bStopRequested = false;
	return true;
}

// A Python single-quoted string literal from a safe token (op name / op_id / base64):
// these charsets never contain a quote, but we defensively strip quotes/backslashes/
// newlines so a hostile op_id can never break out of the literal.
static FString MCPPyLiteral(const FString& In)
{
	FString S = In;
	S.ReplaceInline(TEXT("\\"), TEXT(""));
	S.ReplaceInline(TEXT("'"), TEXT(""));
	S.ReplaceInline(TEXT("\n"), TEXT(""));
	S.ReplaceInline(TEXT("\r"), TEXT(""));
	return FString::Printf(TEXT("'%s'"), *S);
}

void FMCPCockpitServer::WireDefaultDispatcher()
{
	SetDispatcher([this](const FPendingRpc& Rpc)
	{
		IPythonScriptPlugin* Py = IPythonScriptPlugin::Get();
		if (!Py || !Py->IsPythonAvailable())
		{
			SendErrorResult(Rpc.OpId, TEXT("EDITOR_EXEC_FAILED"), TEXT("python unavailable"));
			return;
		}
		// base64 the JSON args (exactly how the uexec path frames them), so the command is
		// pure ASCII with no injection surface — Python does json.loads(base64.b64decode()).
		const FString ArgsJson = Rpc.ArgsJson.IsEmpty() ? TEXT("{}") : Rpc.ArgsJson;
		FTCHARToUTF8 ArgsUtf8(*ArgsJson);
		const FString B64 = FBase64::Encode(reinterpret_cast<const uint8*>(ArgsUtf8.Get()), ArgsUtf8.Length());

		// The bridge is exec'd into __main__ globals (not a module), and ExecPythonCommandEx
		// runs in __main__ too, so _mcp_dispatch_native is reachable as a bare global (§2.1).
		const FString Cmd = FString::Printf(TEXT("_mcp_dispatch_native(%s, %s, %s)"),
			*MCPPyLiteral(Rpc.Op), *MCPPyLiteral(B64), *MCPPyLiteral(Rpc.OpId));

		CurrentDispatchOpId = Rpc.OpId;
		bCurrentEmitted = false;

		FPythonCommandEx PyCmd;
		PyCmd.Command = Cmd;
		PyCmd.ExecutionMode = EPythonCommandExecutionMode::ExecuteStatement;
		const bool bOk = Py->ExecPythonCommandEx(PyCmd);

		// Reconciliation: the op body emits its result from inside Python via emit_result.
		// If that never fired (an exception before _emit, a binding error), synthesize a
		// terminal failure so the rpc can never silently hang (§5.1, fix #10).
		if (!bCurrentEmitted)
		{
			// Belt-and-suspenders (§5.1): if _emit fell back to the stdout marker (its
			// native emit_result raised), the REAL result is in the Info log as
			// "__MCP_JSON__<json>". Forward it rather than inverting a success into a
			// failure. Otherwise accumulate the full Error traceback into EDITOR_EXEC_FAILED.
			static const FString Marker(TEXT("__MCP_JSON__"));
			FString MarkerJson;
			FString ErrLines;
			for (const FPythonLogOutputEntry& E : PyCmd.LogOutput)
			{
				const int32 MPos = E.Output.Find(Marker);
				if (MPos != INDEX_NONE)
				{
					MarkerJson = E.Output.Mid(MPos + Marker.Len());
				}
				if (E.Type == EPythonLogOutputType::Error)
				{
					if (!ErrLines.IsEmpty()) { ErrLines += TEXT("\n"); }
					ErrLines += E.Output;
				}
			}
			if (!MarkerJson.IsEmpty())
			{
				EmitResult(Rpc.OpId, MarkerJson); // real result recovered from the marker fallback
			}
			else
			{
				TSharedRef<FJsonObject> R = MakeShared<FJsonObject>();
				R->SetStringField(TEXT("type"), TEXT("rpc_result"));
				R->SetStringField(TEXT("op_id"), Rpc.OpId);
				R->SetBoolField(TEXT("ok"), false);
				R->SetStringField(TEXT("code"), TEXT("EDITOR_EXEC_FAILED"));
				R->SetStringField(TEXT("error"), ErrLines.IsEmpty() ? TEXT("op did not emit a result") : ErrLines);
				R->SetStringField(TEXT("traceback"), bOk ? TEXT("") : TEXT("ExecPythonCommandEx returned failure"));
				SendFrame(R);
			}
		}
		CurrentDispatchOpId.Reset();
	});
}

void FMCPCockpitServer::Stop()
{
	if (!bRunning) return;
	bRunning = false;
	if (TickHandle.IsValid())
	{
		FTSTicker::GetCoreTicker().RemoveTicker(TickHandle);
		TickHandle.Reset();
	}
	if (Listener)
	{
		Listener->OnConnectionAccepted().Unbind();
		delete Listener; // FTcpListener dtor stops its thread + closes the listen socket
		Listener = nullptr;
	}
	// Ticker + listener are now gone, so no GameThreadTick/OnConnectionAccepted can race:
	// drain a leftover handed-off socket, then tear down the installed peer.
	if (FSocket* Leftover = PendingPeer.Exchange(nullptr))
	{
		Leftover->Close();
		ISocketSubsystem::Get(PLATFORM_SOCKETSUBSYSTEM)->DestroySocket(Leftover);
	}
	ClosePeer();
}

bool FMCPCockpitServer::OnConnectionAccepted(FSocket* InSocket, const FIPv4Endpoint& Endpoint)
{
	// Runs on the FTcpListener thread. Do NOT install/teardown here — just hand the socket
	// to the game thread via PendingPeer so ALL peer lifecycle is single-threaded. If a
	// prior pending socket was never consumed (two accepts before a tick), close it — it
	// was never wired to an rx thread, so closing it directly here is safe.
	FSocket* Superseded = PendingPeer.Exchange(InSocket);
	if (Superseded)
	{
		Superseded->Close();
		ISocketSubsystem::Get(PLATFORM_SOCKETSUBSYSTEM)->DestroySocket(Superseded);
	}
	return true; // accepted; ownership of InSocket transfers to us (installed on the game thread)
}

// GAME THREAD ONLY. Tears down any existing peer, then wires the new socket + its rx thread.
void FMCPCockpitServer::InstallPeer(FSocket* NewSocket)
{
	ClosePeer();                 // safe: same (game) thread, so no self-join / no race
	bPeerRejected = false;       // a fresh peer supersedes any pending rejection latch
	{
		FScopeLock Lock(&PeerCS);
		PeerSocket = NewSocket;
	}
	RxRunnable = new FMCPRxRunnable(this, NewSocket);
	RxThread = FRunnableThread::Create(RxRunnable, TEXT("MCPCockpitRx"));
}

// Teardown of the peer + rx thread. MUST be called from the listener or game thread —
// NEVER the rx thread (that would self-join). Closes the socket FIRST so the rx thread's
// parked Recv/Wait returns, THEN joins the thread, THEN destroys the socket.
void FMCPCockpitServer::ClosePeer()
{
	FSocket* Sock = nullptr;
	{
		FScopeLock Lock(&PeerCS);
		Sock = PeerSocket;
		PeerSocket = nullptr;
	}
	if (Sock)
	{
		Sock->Close(); // unblocks the rx thread's Wait/Recv
	}
	if (RxThread)
	{
		RxThread->Kill(true); // sets bStop + joins; bounded because the socket is closed
		delete RxThread;
		RxThread = nullptr;
	}
	if (RxRunnable)
	{
		delete RxRunnable;
		RxRunnable = nullptr;
	}
	if (Sock)
	{
		ISocketSubsystem::Get(PLATFORM_SOCKETSUBSYSTEM)->DestroySocket(Sock);
	}
	// The rx thread (sole owner of the parked-gate maps) is now joined, so clearing them is
	// race-free. Drop this session's parked gates so a reconnect doesn't leak them or later
	// resolve a stale op into the game thread (the agent sees EDITOR_RESET on reconnect).
	ParkedGates.Empty();
	OpToGate.Empty();
}

bool FMCPCockpitServer::SendFrame(const TSharedRef<FJsonObject>& Frame)
{
	TArray<uint8> Buf;
	MCPBuildFrameBytes(Frame, Buf);
	FScopeLock Lock(&PeerCS);
	if (!PeerSocket) return false;
	return MCPSendAll(PeerSocket, Buf.GetData(), Buf.Num());
}

// Called from the rx thread for each inbound hello/rpc/control frame. Returns false to
// stop reading (reject). Teardown of a rejected peer is deferred to the game thread via
// bPeerRejected so the rx thread never self-joins.
bool FMCPCockpitServer::HandleInboundFrame(const FString& Json)
{
	TSharedPtr<FJsonObject> F = MCPJsonParse(Json);
	if (!F.IsValid()) return true; // ignore a garbage frame, keep reading
	FString Type;
	F->TryGetStringField(TEXT("type"), Type);

	if (Type == TEXT("hello"))
	{
		FString PeerToken;
		F->TryGetStringField(TEXT("token"), PeerToken);
		if (!Token.IsEmpty() && PeerToken != Token)
		{
			bPeerRejected = true; // game thread will ClosePeer (§2.5 step 5, deadlock-free)
			return false;
		}
		TSharedRef<FJsonObject> W = MakeShared<FJsonObject>();
		W->SetStringField(TEXT("type"), TEXT("welcome"));
		W->SetStringField(TEXT("session_epoch"), SessionEpoch);
		W->SetStringField(TEXT("manifest_digest"), ManifestDigest); // empty in A0; real in B2
		W->SetStringField(TEXT("engine_version"), FEngineVersion::Current().ToString());
		W->SetStringField(TEXT("project"), FPaths::GetProjectFilePath());
		W->SetNumberField(TEXT("protocol_version"), MCP_PROTOCOL_VERSION);
		SendFrame(W);
		double LastSeen = 0;
		if (F->TryGetNumberField(TEXT("last_seen_seq"), LastSeen) && LastSeen > 0)
		{
			ServeReplayFrom((uint64)LastSeen);
		}
		return true;
	}
	if (Type == TEXT("rpc"))
	{
		FPendingRpc Rpc;
		F->TryGetStringField(TEXT("op_id"), Rpc.OpId);
		F->TryGetStringField(TEXT("op"), Rpc.Op);
		F->TryGetStringField(TEXT("intent"), Rpc.Intent);
		F->TryGetStringField(TEXT("task_id"), Rpc.TaskId);
		const TSharedPtr<FJsonObject>* ArgsObj = nullptr;
		if (F->TryGetObjectField(TEXT("args"), ArgsObj) && ArgsObj)
		{
			Rpc.ArgsJson = MCPJsonObjectToString((*ArgsObj).ToSharedRef());
		}
		// Human-in-the-loop: a gate-flagged op is PARKED (not run) until a control
		// approve/deny/cancel resolves it (§4.2). Go sets gate=true from the manifest+policy.
		bool bGate = false;
		F->TryGetBoolField(TEXT("gate"), bGate);
		if (bGate)
		{
			FString Classification;
			F->TryGetStringField(TEXT("classification"), Classification);
			ParkGate(Rpc, Classification);
			return true;
		}
		// Bounded queue: reject beyond depth D with QUEUE_FULL (§5.6).
		if (RpcQueueLen.Load() >= RpcQueueDepth)
		{
			SendErrorResult(Rpc.OpId, TEXT("QUEUE_FULL"), TEXT("rpc queue full"));
			return true;
		}
		RpcQueueLen.IncrementExchange();
		RpcQueue.Enqueue(MoveTemp(Rpc));
		return true;
	}
	if (Type == TEXT("control"))
	{
		FString Ctrl;
		F->TryGetStringField(TEXT("control"), Ctrl);
		if (Ctrl == TEXT("stop"))
		{
			bStopRequested = true; // off-thread-immediate latch (§2.4)
		}
		else if (Ctrl == TEXT("replay_from"))
		{
			double FromSeq = 0;
			F->TryGetNumberField(TEXT("from_seq"), FromSeq);
			ServeReplayFrom((uint64)FromSeq);
		}
		else if (Ctrl == TEXT("approve"))
		{
			FString GateId;
			F->TryGetStringField(TEXT("gate_id"), GateId);
			ResolveGate(GateId, /*bApprove=*/true); // run the parked op now
		}
		else if (Ctrl == TEXT("deny"))
		{
			FString GateId;
			F->TryGetStringField(TEXT("gate_id"), GateId);
			ResolveGate(GateId, /*bApprove=*/false); // drop with DENIED
		}
		else if (Ctrl == TEXT("cancel"))
		{
			// A parked gate whose agent departed (sever) is dropped; a queued/running op
			// cannot be preempted mid-flight (§2.6), so cancel only affects parked gates.
			FString OpId;
			F->TryGetStringField(TEXT("op_id"), OpId);
			if (const FString* GateId = OpToGate.Find(OpId))
			{
				ResolveGate(*GateId, /*bApprove=*/false);
			}
		}
		return true;
	}
	return true;
}

void FMCPCockpitServer::SendErrorResult(const FString& OpId, const FString& Code, const FString& Error)
{
	TSharedRef<FJsonObject> R = MakeShared<FJsonObject>();
	R->SetStringField(TEXT("type"), TEXT("rpc_result"));
	R->SetStringField(TEXT("op_id"), OpId);
	R->SetBoolField(TEXT("ok"), false);
	R->SetStringField(TEXT("code"), Code);
	R->SetStringField(TEXT("error"), Error);
	SendFrame(R);
}

// ParkGate holds a gate-flagged rpc (not run) and emits a gate frame for the human (§4.2).
// rx-thread only, so the maps need no lock. The before→after diff is a follow-on.
void FMCPCockpitServer::ParkGate(const FPendingRpc& Rpc, const FString& Classification)
{
	// Reject a duplicate op_id so cancel-by-op_id can always reach the right gate and we
	// never orphan a parked entry.
	if (OpToGate.Contains(Rpc.OpId))
	{
		SendErrorResult(Rpc.OpId, TEXT("DUP_OP"), TEXT("op_id already parked"));
		return;
	}
	const FString GateId = FGuid::NewGuid().ToString(EGuidFormats::Digits);
	// Hash the UTF-8 bytes (not TCHAR_TO_ANSI, which collapses non-ASCII to '?') so distinct
	// Unicode asset names produce distinct args_hashes.
	FTCHARToUTF8 ArgsUtf8(*Rpc.ArgsJson);
	FMD5 Md5;
	Md5.Update(reinterpret_cast<const uint8*>(ArgsUtf8.Get()), ArgsUtf8.Length());
	uint8 Digest[16];
	Md5.Final(Digest);
	const FString ArgsHash = BytesToHex(Digest, 16);
	ParkedGates.Add(GateId, Rpc);
	OpToGate.Add(Rpc.OpId, GateId);

	TSharedRef<FJsonObject> G = MakeShared<FJsonObject>();
	G->SetStringField(TEXT("type"), TEXT("gate"));
	G->SetStringField(TEXT("gate_id"), GateId);
	G->SetStringField(TEXT("op_id"), Rpc.OpId);
	G->SetStringField(TEXT("op"), Rpc.Op);
	G->SetStringField(TEXT("classification"), Classification);
	G->SetStringField(TEXT("args_hash"), ArgsHash);
	SendFrame(G);
}

// ResolveGate runs (approve → enqueue for the game thread) or drops (deny → DENIED) a
// parked gate. rx-thread only. GateId is by VALUE (the cancel path's ref lives inside
// OpToGate, which we erase). A missing gate_id is a no-op (already resolved/severed).
void FMCPCockpitServer::ResolveGate(FString GateId, bool bApprove)
{
	FPendingRpc* Found = ParkedGates.Find(GateId);
	if (!Found)
	{
		return;
	}
	FPendingRpc Rpc = *Found; // copy before erasing
	OpToGate.Remove(Rpc.OpId);
	ParkedGates.Remove(GateId);

	if (!bApprove)
	{
		SendErrorResult(Rpc.OpId, TEXT("DENIED"), TEXT("gate denied"));
		return;
	}
	if (RpcQueueLen.Load() >= RpcQueueDepth)
	{
		SendErrorResult(Rpc.OpId, TEXT("QUEUE_FULL"), TEXT("rpc queue full at approve"));
		return;
	}
	RpcQueueLen.IncrementExchange();
	RpcQueue.Enqueue(MoveTemp(Rpc)); // dispatched on the next game-thread tick
}

// Exactly one rpc executes at a time (§5.6). Popped + dispatched on the game thread.
bool FMCPCockpitServer::GameThreadTick(float Dt)
{
	if (!bRunning) return false;
	// Install a newly-accepted peer (handed off by the listener thread), single-threaded.
	if (FSocket* NewPeer = PendingPeer.Exchange(nullptr))
	{
		InstallPeer(NewPeer);
	}
	if (bPeerRejected.Exchange(false))
	{
		ClosePeer(); // deferred teardown of a token-rejected peer, safely on the game thread
	}
	if (bRpcExecuting.Load()) return true;
	FPendingRpc Rpc;
	if (RpcQueue.Dequeue(Rpc))
	{
		RpcQueueLen.DecrementExchange();
		if (bStopRequested.Load())
		{
			SendErrorResult(Rpc.OpId, TEXT("CANCELLED"), TEXT("stop latched")); // drain queue on stop
			return true;
		}
		bRpcExecuting = true;
		if (Dispatcher)
		{
			Dispatcher(Rpc); // MUST EmitResult(OpId) exactly once (B1 reconciles)
		}
		else
		{
			SendErrorResult(Rpc.OpId, TEXT("NO_DISPATCHER"), TEXT("no op dispatcher (A0)"));
		}
		bRpcExecuting = false;
	}
	return true;
}

void FMCPCockpitServer::EmitEvent(const FString& EventType, const FString& PayloadJson)
{
	const uint64 Seq = (uint64)SeqCounter.Increment();
	TSharedRef<FJsonObject> E = MakeShared<FJsonObject>();
	E->SetStringField(TEXT("type"), TEXT("event"));
	E->SetNumberField(TEXT("seq"), Seq);
	E->SetStringField(TEXT("event_type"), EventType);
	TSharedPtr<FJsonObject> P = MCPJsonParse(PayloadJson.IsEmpty() ? TEXT("{}") : PayloadJson);
	if (P.IsValid()) E->SetObjectField(TEXT("payload"), P);
	{
		FScopeLock Lock(&RingCS);
		if (DroppedSinceEmit > 0) { E->SetNumberField(TEXT("dropped"), (double)DroppedSinceEmit); DroppedSinceEmit = 0; }
	}
	PushToRing(Seq, MCPJsonObjectToString(E));
	SendFrame(E);
}

void FMCPCockpitServer::EmitProgress(const FString& OpId, const FString& PayloadJson)
{
	const uint64 Seq = (uint64)SeqCounter.Increment();
	TSharedRef<FJsonObject> E = MakeShared<FJsonObject>();
	E->SetStringField(TEXT("type"), TEXT("progress"));
	E->SetNumberField(TEXT("seq"), Seq);
	E->SetStringField(TEXT("op_id"), OpId);
	TSharedPtr<FJsonObject> P = MCPJsonParse(PayloadJson.IsEmpty() ? TEXT("{}") : PayloadJson);
	if (P.IsValid()) E->SetObjectField(TEXT("payload"), P);
	SendFrame(E);
}

// EmitResult flattens the Python op envelope {ok,result,error,code,retryable,traceback}
// to TOP-LEVEL frame fields so it matches the flat Go Frame exactly (§5.1).
void FMCPCockpitServer::EmitResult(const FString& OpId, const FString& ResultEnvelopeJson)
{
	// Reconciliation bookkeeping: a real emit for the in-flight op cancels the synthesized
	// EDITOR_EXEC_FAILED (both run on the game thread, so no atomicity needed).
	if (!CurrentDispatchOpId.IsEmpty() && OpId == CurrentDispatchOpId)
	{
		bCurrentEmitted = true;
	}
	TSharedPtr<FJsonObject> Env = MCPJsonParse(ResultEnvelopeJson);
	TSharedRef<FJsonObject> R = MakeShared<FJsonObject>();
	R->SetStringField(TEXT("type"), TEXT("rpc_result"));
	R->SetStringField(TEXT("op_id"), OpId);
	if (Env.IsValid())
	{
		bool bOk = true;
		if (Env->HasTypedField<EJson::Boolean>(TEXT("ok"))) { bOk = Env->GetBoolField(TEXT("ok")); }
		else { bOk = !Env->HasField(TEXT("error")); }
		R->SetBoolField(TEXT("ok"), bOk);

		const TSharedPtr<FJsonObject>* ResultObj = nullptr;
		if (Env->TryGetObjectField(TEXT("result"), ResultObj) && ResultObj)
		{
			R->SetObjectField(TEXT("result"), *ResultObj);
		}
		else if (!Env->HasField(TEXT("ok")) && !Env->HasField(TEXT("error")))
		{
			// A bare payload with no envelope wrapper → treat the whole object as result.
			R->SetObjectField(TEXT("result"), Env);
		}
		FString S;
		if (Env->TryGetStringField(TEXT("error"), S)) R->SetStringField(TEXT("error"), S);
		if (Env->TryGetStringField(TEXT("code"), S)) R->SetStringField(TEXT("code"), S);
		if (Env->TryGetStringField(TEXT("traceback"), S)) R->SetStringField(TEXT("traceback"), S);
		bool bRetry = false;
		if (Env->TryGetBoolField(TEXT("retryable"), bRetry) && bRetry) R->SetBoolField(TEXT("retryable"), true);
	}
	else
	{
		R->SetBoolField(TEXT("ok"), false);
		R->SetStringField(TEXT("code"), TEXT("EDITOR_EXEC_FAILED"));
		R->SetStringField(TEXT("error"), TEXT("unparseable result envelope"));
	}
	SendFrame(R);
}

void FMCPCockpitServer::PushToRing(uint64 Seq, const FString& FrameJson)
{
	FScopeLock Lock(&RingCS);
	if (Ring.Num() < EventRingCapacity)
	{
		Ring.Add({ Seq, FrameJson });
		if (RingFloor == 0) RingFloor = Seq;
	}
	else
	{
		// drop-oldest
		RingFloor = Ring[RingHead].Seq + 1;
		Ring[RingHead] = { Seq, FrameJson };
		RingHead = (RingHead + 1) % EventRingCapacity;
		DroppedSinceEmit++;
	}
}

void FMCPCockpitServer::ServeReplayFrom(uint64 FromSeq)
{
	TArray<FRingEntry> Entries;
	{
		FScopeLock Lock(&RingCS);
		for (int32 i = 0; i < Ring.Num(); ++i)
		{
			if (Ring[i].Seq > FromSeq) Entries.Add(Ring[i]);
		}
	}
	// Ordered single-seq-space replay: sort NUMERICALLY by seq (not lexically).
	Entries.Sort([](const FRingEntry& A, const FRingEntry& B) { return A.Seq < B.Seq; });

	FScopeLock Lock(&PeerCS);
	if (!PeerSocket) return;
	for (const FRingEntry& E : Entries)
	{
		FTCHARToUTF8 Utf8(*E.Json);
		const int32 Len = Utf8.Length();
		TArray<uint8> Buf; Buf.SetNumUninitialized(4 + Len);
		Buf[0] = (Len >> 24) & 0xFF; Buf[1] = (Len >> 16) & 0xFF; Buf[2] = (Len >> 8) & 0xFF; Buf[3] = Len & 0xFF;
		FMemory::Memcpy(Buf.GetData() + 4, Utf8.Get(), Len);
		if (!MCPSendAll(PeerSocket, Buf.GetData(), Buf.Num())) break;
	}
}
