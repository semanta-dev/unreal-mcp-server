// Copyright unreal-mcp-server. MIT.
#include "MCPCockpitServer.h"

#include "Common/TcpListener.h"
#include "Sockets.h"
#include "SocketSubsystem.h"
#include "Interfaces/IPv4/IPv4Endpoint.h"
#include "HAL/RunnableThread.h"
#include "Serialization/JsonSerializer.h"
#include "Serialization/JsonWriter.h"
#include "Dom/JsonObject.h"
#include "Misc/Guid.h"

static const int32 MCP_PROTOCOL_VERSION = 1;

// ---------------------------------------------------------------------------
// Framing helpers: 4-byte big-endian length prefix + JSON body, matching the Go
// internal/cockpit codec exactly.
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

// Blocking read of exactly Num bytes from Socket into Dst. Returns false on error/close.
static bool MCPRecvExact(FSocket* Socket, uint8* Dst, int32 Num)
{
	int32 Total = 0;
	while (Total < Num)
	{
		int32 Read = 0;
		if (!Socket->Recv(Dst + Total, Num - Total, Read) || Read <= 0)
		{
			return false;
		}
		Total += Read;
	}
	return true;
}

// Rx thread: reads framed hello/rpc/control from the peer and hands them to the server.
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
			if (!MCPRecvExact(Socket, Hdr, 4)) break;
			const uint32 Len = (uint32(Hdr[0]) << 24) | (uint32(Hdr[1]) << 16) | (uint32(Hdr[2]) << 8) | uint32(Hdr[3]);
			if (Len == 0 || Len > (64u << 20)) break; // matches Go MaxFrameBytes
			TArray<uint8> Body;
			Body.SetNumUninitialized(Len);
			if (!MCPRecvExact(Socket, Body.GetData(), Len)) break;
			FString Json(Len, reinterpret_cast<const char*>(Body.GetData())); // UTF8 payload
			Server->HandleInboundFrame(Json);
		}
		return 0;
	}
private:
	FMCPCockpitServer* Server;
	FSocket* Socket;
	TAtomic<bool> bStop{ false };
};

FMCPCockpitServer& FMCPCockpitServer::Get()
{
	static FMCPCockpitServer* Singleton = new FMCPCockpitServer(); // never destroyed → survives Live Coding
	return *Singleton;
}

FMCPCockpitServer::~FMCPCockpitServer() { Stop(); }

int32 FMCPCockpitServer::GetProtocolVersion() { return MCP_PROTOCOL_VERSION; }

bool FMCPCockpitServer::Start()
{
	if (bRunning) return true;
	SessionEpoch = FGuid::NewGuid().ToString(EGuidFormats::DigitsWithHyphens);
	Token = FGuid::NewGuid().ToString(EGuidFormats::Digits);
	Ring.Reset();
	RingHead = 0;
	RingFloor = 0;

	// Ephemeral loopback listener (127.0.0.1:0). FTcpListener creates+binds+listens on
	// its own thread and fires OnConnectionAccepted per peer.
	Listener = new FTcpListener(FIPv4Endpoint(FIPv4Address::InternalLoopback, 0));
	if (!Listener->GetSocket())
	{
		delete Listener;
		Listener = nullptr;
		return false;
	}
	Listener->OnConnectionAccepted().BindRaw(this, &FMCPCockpitServer::OnConnectionAccepted);

	// Resolve the OS-assigned port from the bound listen socket.
	TSharedRef<FInternetAddr> Addr = ISocketSubsystem::Get(PLATFORM_SOCKETSUBSYSTEM)->CreateInternetAddr();
	Listener->GetSocket()->GetAddress(*Addr);
	Port = Addr->GetPort();

	TickHandle = FTSTicker::GetCoreTicker().AddTicker(
		FTickerDelegate::CreateRaw(this, &FMCPCockpitServer::GameThreadTick), 0.0f);

	bRunning = true;
	bStopRequested = false;
	return true;
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
	ClosePeer();
}

bool FMCPCockpitServer::OnConnectionAccepted(FSocket* InSocket, const FIPv4Endpoint& Endpoint)
{
	// One peer at a time; a new connection replaces the old (Go re-dials on reconnect).
	ClosePeer();
	{
		FScopeLock Lock(&PeerCS);
		PeerSocket = InSocket;
	}
	RxRunnable = new FMCPRxRunnable(this, InSocket);
	RxThread = FRunnableThread::Create(RxRunnable, TEXT("MCPCockpitRx"));
	return true; // accepted; FTcpListener hands ownership of the socket to us
}

void FMCPCockpitServer::ClosePeer()
{
	if (RxThread)
	{
		RxThread->Kill(true);
		delete RxThread;
		RxThread = nullptr;
	}
	if (RxRunnable)
	{
		delete RxRunnable;
		RxRunnable = nullptr;
	}
	FScopeLock Lock(&PeerCS);
	if (PeerSocket)
	{
		PeerSocket->Close();
		ISocketSubsystem::Get(PLATFORM_SOCKETSUBSYSTEM)->DestroySocket(PeerSocket);
		PeerSocket = nullptr;
	}
}

bool FMCPCockpitServer::SendFrame(const TSharedRef<FJsonObject>& Frame)
{
	const FString Json = MCPJsonObjectToString(Frame);
	FTCHARToUTF8 Utf8(*Json);
	const int32 Len = Utf8.Length();
	TArray<uint8> Buf;
	Buf.SetNumUninitialized(4 + Len);
	Buf[0] = (Len >> 24) & 0xFF; Buf[1] = (Len >> 16) & 0xFF; Buf[2] = (Len >> 8) & 0xFF; Buf[3] = Len & 0xFF;
	FMemory::Memcpy(Buf.GetData() + 4, Utf8.Get(), Len);

	FScopeLock Lock(&PeerCS);
	if (!PeerSocket) return false;
	int32 Sent = 0;
	return PeerSocket->Send(Buf.GetData(), Buf.Num(), Sent) && Sent == Buf.Num();
}

// Called from the rx thread for each inbound hello/rpc/control frame.
void FMCPCockpitServer::HandleInboundFrame(const FString& Json)
{
	TSharedPtr<FJsonObject> F = MCPJsonParse(Json);
	if (!F.IsValid()) return;
	const FString Type = F->GetStringField(TEXT("type"));

	if (Type == TEXT("hello"))
	{
		// Token check (loopback parity with StrictNode); reply welcome.
		const FString PeerToken = F->GetStringField(TEXT("token"));
		if (!Token.IsEmpty() && PeerToken != Token)
		{
			ClosePeer();
			return;
		}
		TSharedRef<FJsonObject> W = MakeShared<FJsonObject>();
		W->SetStringField(TEXT("type"), TEXT("welcome"));
		W->SetStringField(TEXT("session_epoch"), SessionEpoch);
		W->SetNumberField(TEXT("protocol_version"), MCP_PROTOCOL_VERSION);
		SendFrame(W);
		// Serve any requested replay gap.
		uint64 LastSeen = (uint64)F->GetNumberField(TEXT("last_seen_seq"));
		if (LastSeen > 0) ServeReplayFrom(LastSeen);
	}
	else if (Type == TEXT("rpc"))
	{
		FPendingRpc Rpc;
		Rpc.OpId = F->GetStringField(TEXT("op_id"));
		Rpc.Op = F->GetStringField(TEXT("op"));
		Rpc.Intent = F->GetStringField(TEXT("intent"));
		Rpc.TaskId = F->GetStringField(TEXT("task_id"));
		const TSharedPtr<FJsonObject>* ArgsObj;
		if (F->TryGetObjectField(TEXT("args"), ArgsObj))
		{
			Rpc.ArgsJson = MCPJsonObjectToString((*ArgsObj).ToSharedRef());
		}
		// Bounded queue: reject beyond depth D with QUEUE_FULL (§5.6).
		if (RpcQueueLen.Load() >= RpcQueueDepth)
		{
			TSharedRef<FJsonObject> R = MakeShared<FJsonObject>();
			R->SetStringField(TEXT("type"), TEXT("rpc_result"));
			R->SetStringField(TEXT("op_id"), Rpc.OpId);
			R->SetBoolField(TEXT("ok"), false);
			R->SetStringField(TEXT("code"), TEXT("QUEUE_FULL"));
			R->SetStringField(TEXT("error"), TEXT("rpc queue full"));
			SendFrame(R);
			return;
		}
		RpcQueueLen.IncrementExchange();
		RpcQueue.Enqueue(MoveTemp(Rpc));
	}
	else if (Type == TEXT("control"))
	{
		const FString Ctrl = F->GetStringField(TEXT("control"));
		if (Ctrl == TEXT("stop"))
		{
			bStopRequested = true; // off-thread-immediate latch (§2.4)
		}
		else if (Ctrl == TEXT("replay_from"))
		{
			ServeReplayFrom((uint64)F->GetNumberField(TEXT("from_seq")));
		}
		// cancel/pause/approve/… are wired in Phase C.
	}
}

// Exactly one rpc executes at a time (§5.6). Popped + dispatched on the game thread.
bool FMCPCockpitServer::GameThreadTick(float Dt)
{
	if (!bRunning) return false;
	if (bRpcExecuting.Load()) return true;
	FPendingRpc Rpc;
	if (RpcQueue.Dequeue(Rpc))
	{
		RpcQueueLen.DecrementExchange();
		if (bStopRequested.Load())
		{
			// Drain queued rpcs as CANCELLED while a stop is latched.
			TSharedRef<FJsonObject> R = MakeShared<FJsonObject>();
			R->SetStringField(TEXT("type"), TEXT("rpc_result"));
			R->SetStringField(TEXT("op_id"), Rpc.OpId);
			R->SetBoolField(TEXT("ok"), false);
			R->SetStringField(TEXT("code"), TEXT("CANCELLED"));
			SendFrame(R);
			return true;
		}
		bRpcExecuting = true;
		if (Dispatcher)
		{
			Dispatcher(Rpc); // MUST EmitResult(OpId) exactly once (B1 reconciles)
		}
		else
		{
			// A0: no dispatcher wired → a placeholder terminal result so nothing hangs.
			TSharedRef<FJsonObject> R = MakeShared<FJsonObject>();
			R->SetStringField(TEXT("type"), TEXT("rpc_result"));
			R->SetStringField(TEXT("op_id"), Rpc.OpId);
			R->SetBoolField(TEXT("ok"), false);
			R->SetStringField(TEXT("code"), TEXT("NO_DISPATCHER"));
			SendFrame(R);
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

void FMCPCockpitServer::EmitResult(const FString& OpId, const FString& ResultEnvelopeJson)
{
	TSharedPtr<FJsonObject> Env = MCPJsonParse(ResultEnvelopeJson);
	TSharedRef<FJsonObject> R = MakeShared<FJsonObject>();
	R->SetStringField(TEXT("type"), TEXT("rpc_result"));
	R->SetStringField(TEXT("op_id"), OpId);
	if (Env.IsValid())
	{
		R->SetBoolField(TEXT("ok"), Env->HasField(TEXT("error")) == false);
		R->SetObjectField(TEXT("result"), Env);
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
	TArray<FString> ToSend;
	{
		FScopeLock Lock(&RingCS);
		for (int32 i = 0; i < Ring.Num(); ++i)
		{
			const FRingEntry& E = Ring[i];
			if (E.Seq > FromSeq) ToSend.Add(E.Json);
		}
	}
	ToSend.Sort(); // seq is embedded; entries beyond the ring floor are lost (journal is B2)
	FScopeLock Lock(&PeerCS);
	if (!PeerSocket) return;
	for (const FString& Json : ToSend)
	{
		FTCHARToUTF8 Utf8(*Json);
		const int32 Len = Utf8.Length();
		TArray<uint8> Buf; Buf.SetNumUninitialized(4 + Len);
		Buf[0] = (Len >> 24) & 0xFF; Buf[1] = (Len >> 16) & 0xFF; Buf[2] = (Len >> 8) & 0xFF; Buf[3] = Len & 0xFF;
		FMemory::Memcpy(Buf.GetData() + 4, Utf8.Get(), Len);
		int32 Sent = 0;
		PeerSocket->Send(Buf.GetData(), Buf.Num(), Sent);
	}
}
