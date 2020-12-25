// Generated by the gRPC C++ plugin.
// If you make any local change, they will be lost.
// source: remotexec.proto
#ifndef GRPC_remotexec_2eproto__INCLUDED
#define GRPC_remotexec_2eproto__INCLUDED

#include "remotexec.pb.h"

#include <functional>
#include <grpc/impl/codegen/port_platform.h>
#include <grpcpp/impl/codegen/async_generic_service.h>
#include <grpcpp/impl/codegen/async_stream.h>
#include <grpcpp/impl/codegen/async_unary_call.h>
#include <grpcpp/impl/codegen/client_callback.h>
#include <grpcpp/impl/codegen/client_context.h>
#include <grpcpp/impl/codegen/completion_queue.h>
#include <grpcpp/impl/codegen/message_allocator.h>
#include <grpcpp/impl/codegen/method_handler.h>
#include <grpcpp/impl/codegen/proto_utils.h>
#include <grpcpp/impl/codegen/rpc_method.h>
#include <grpcpp/impl/codegen/server_callback.h>
#include <grpcpp/impl/codegen/server_callback_handlers.h>
#include <grpcpp/impl/codegen/server_context.h>
#include <grpcpp/impl/codegen/service_type.h>
#include <grpcpp/impl/codegen/status.h>
#include <grpcpp/impl/codegen/stub_options.h>
#include <grpcpp/impl/codegen/sync_stream.h>

namespace com {
namespace ibm {
namespace cloudland {
namespace scripts {

class RemoteExec final {
 public:
  static constexpr char const* service_full_name() {
    return "com.ibm.cloudland.scripts.RemoteExec";
  }
  class StubInterface {
   public:
    virtual ~StubInterface() {}
    virtual ::grpc::Status Execute(::grpc::ClientContext* context, const ::com::ibm::cloudland::scripts::ExecuteRequest& request, ::com::ibm::cloudland::scripts::ExecuteReply* response) = 0;
    std::unique_ptr< ::grpc::ClientAsyncResponseReaderInterface< ::com::ibm::cloudland::scripts::ExecuteReply>> AsyncExecute(::grpc::ClientContext* context, const ::com::ibm::cloudland::scripts::ExecuteRequest& request, ::grpc::CompletionQueue* cq) {
      return std::unique_ptr< ::grpc::ClientAsyncResponseReaderInterface< ::com::ibm::cloudland::scripts::ExecuteReply>>(AsyncExecuteRaw(context, request, cq));
    }
    std::unique_ptr< ::grpc::ClientAsyncResponseReaderInterface< ::com::ibm::cloudland::scripts::ExecuteReply>> PrepareAsyncExecute(::grpc::ClientContext* context, const ::com::ibm::cloudland::scripts::ExecuteRequest& request, ::grpc::CompletionQueue* cq) {
      return std::unique_ptr< ::grpc::ClientAsyncResponseReaderInterface< ::com::ibm::cloudland::scripts::ExecuteReply>>(PrepareAsyncExecuteRaw(context, request, cq));
    }
    std::unique_ptr< ::grpc::ClientWriterInterface< ::com::ibm::cloudland::scripts::FileChunk>> Transmit(::grpc::ClientContext* context, ::com::ibm::cloudland::scripts::TransmitAck* response) {
      return std::unique_ptr< ::grpc::ClientWriterInterface< ::com::ibm::cloudland::scripts::FileChunk>>(TransmitRaw(context, response));
    }
    std::unique_ptr< ::grpc::ClientAsyncWriterInterface< ::com::ibm::cloudland::scripts::FileChunk>> AsyncTransmit(::grpc::ClientContext* context, ::com::ibm::cloudland::scripts::TransmitAck* response, ::grpc::CompletionQueue* cq, void* tag) {
      return std::unique_ptr< ::grpc::ClientAsyncWriterInterface< ::com::ibm::cloudland::scripts::FileChunk>>(AsyncTransmitRaw(context, response, cq, tag));
    }
    std::unique_ptr< ::grpc::ClientAsyncWriterInterface< ::com::ibm::cloudland::scripts::FileChunk>> PrepareAsyncTransmit(::grpc::ClientContext* context, ::com::ibm::cloudland::scripts::TransmitAck* response, ::grpc::CompletionQueue* cq) {
      return std::unique_ptr< ::grpc::ClientAsyncWriterInterface< ::com::ibm::cloudland::scripts::FileChunk>>(PrepareAsyncTransmitRaw(context, response, cq));
    }
    class experimental_async_interface {
     public:
      virtual ~experimental_async_interface() {}
      virtual void Execute(::grpc::ClientContext* context, const ::com::ibm::cloudland::scripts::ExecuteRequest* request, ::com::ibm::cloudland::scripts::ExecuteReply* response, std::function<void(::grpc::Status)>) = 0;
      virtual void Execute(::grpc::ClientContext* context, const ::grpc::ByteBuffer* request, ::com::ibm::cloudland::scripts::ExecuteReply* response, std::function<void(::grpc::Status)>) = 0;
      #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
      virtual void Execute(::grpc::ClientContext* context, const ::com::ibm::cloudland::scripts::ExecuteRequest* request, ::com::ibm::cloudland::scripts::ExecuteReply* response, ::grpc::ClientUnaryReactor* reactor) = 0;
      #else
      virtual void Execute(::grpc::ClientContext* context, const ::com::ibm::cloudland::scripts::ExecuteRequest* request, ::com::ibm::cloudland::scripts::ExecuteReply* response, ::grpc::experimental::ClientUnaryReactor* reactor) = 0;
      #endif
      #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
      virtual void Execute(::grpc::ClientContext* context, const ::grpc::ByteBuffer* request, ::com::ibm::cloudland::scripts::ExecuteReply* response, ::grpc::ClientUnaryReactor* reactor) = 0;
      #else
      virtual void Execute(::grpc::ClientContext* context, const ::grpc::ByteBuffer* request, ::com::ibm::cloudland::scripts::ExecuteReply* response, ::grpc::experimental::ClientUnaryReactor* reactor) = 0;
      #endif
      #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
      virtual void Transmit(::grpc::ClientContext* context, ::com::ibm::cloudland::scripts::TransmitAck* response, ::grpc::ClientWriteReactor< ::com::ibm::cloudland::scripts::FileChunk>* reactor) = 0;
      #else
      virtual void Transmit(::grpc::ClientContext* context, ::com::ibm::cloudland::scripts::TransmitAck* response, ::grpc::experimental::ClientWriteReactor< ::com::ibm::cloudland::scripts::FileChunk>* reactor) = 0;
      #endif
    };
    #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
    typedef class experimental_async_interface async_interface;
    #endif
    #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
    async_interface* async() { return experimental_async(); }
    #endif
    virtual class experimental_async_interface* experimental_async() { return nullptr; }
  private:
    virtual ::grpc::ClientAsyncResponseReaderInterface< ::com::ibm::cloudland::scripts::ExecuteReply>* AsyncExecuteRaw(::grpc::ClientContext* context, const ::com::ibm::cloudland::scripts::ExecuteRequest& request, ::grpc::CompletionQueue* cq) = 0;
    virtual ::grpc::ClientAsyncResponseReaderInterface< ::com::ibm::cloudland::scripts::ExecuteReply>* PrepareAsyncExecuteRaw(::grpc::ClientContext* context, const ::com::ibm::cloudland::scripts::ExecuteRequest& request, ::grpc::CompletionQueue* cq) = 0;
    virtual ::grpc::ClientWriterInterface< ::com::ibm::cloudland::scripts::FileChunk>* TransmitRaw(::grpc::ClientContext* context, ::com::ibm::cloudland::scripts::TransmitAck* response) = 0;
    virtual ::grpc::ClientAsyncWriterInterface< ::com::ibm::cloudland::scripts::FileChunk>* AsyncTransmitRaw(::grpc::ClientContext* context, ::com::ibm::cloudland::scripts::TransmitAck* response, ::grpc::CompletionQueue* cq, void* tag) = 0;
    virtual ::grpc::ClientAsyncWriterInterface< ::com::ibm::cloudland::scripts::FileChunk>* PrepareAsyncTransmitRaw(::grpc::ClientContext* context, ::com::ibm::cloudland::scripts::TransmitAck* response, ::grpc::CompletionQueue* cq) = 0;
  };
  class Stub final : public StubInterface {
   public:
    Stub(const std::shared_ptr< ::grpc::ChannelInterface>& channel);
    ::grpc::Status Execute(::grpc::ClientContext* context, const ::com::ibm::cloudland::scripts::ExecuteRequest& request, ::com::ibm::cloudland::scripts::ExecuteReply* response) override;
    std::unique_ptr< ::grpc::ClientAsyncResponseReader< ::com::ibm::cloudland::scripts::ExecuteReply>> AsyncExecute(::grpc::ClientContext* context, const ::com::ibm::cloudland::scripts::ExecuteRequest& request, ::grpc::CompletionQueue* cq) {
      return std::unique_ptr< ::grpc::ClientAsyncResponseReader< ::com::ibm::cloudland::scripts::ExecuteReply>>(AsyncExecuteRaw(context, request, cq));
    }
    std::unique_ptr< ::grpc::ClientAsyncResponseReader< ::com::ibm::cloudland::scripts::ExecuteReply>> PrepareAsyncExecute(::grpc::ClientContext* context, const ::com::ibm::cloudland::scripts::ExecuteRequest& request, ::grpc::CompletionQueue* cq) {
      return std::unique_ptr< ::grpc::ClientAsyncResponseReader< ::com::ibm::cloudland::scripts::ExecuteReply>>(PrepareAsyncExecuteRaw(context, request, cq));
    }
    std::unique_ptr< ::grpc::ClientWriter< ::com::ibm::cloudland::scripts::FileChunk>> Transmit(::grpc::ClientContext* context, ::com::ibm::cloudland::scripts::TransmitAck* response) {
      return std::unique_ptr< ::grpc::ClientWriter< ::com::ibm::cloudland::scripts::FileChunk>>(TransmitRaw(context, response));
    }
    std::unique_ptr< ::grpc::ClientAsyncWriter< ::com::ibm::cloudland::scripts::FileChunk>> AsyncTransmit(::grpc::ClientContext* context, ::com::ibm::cloudland::scripts::TransmitAck* response, ::grpc::CompletionQueue* cq, void* tag) {
      return std::unique_ptr< ::grpc::ClientAsyncWriter< ::com::ibm::cloudland::scripts::FileChunk>>(AsyncTransmitRaw(context, response, cq, tag));
    }
    std::unique_ptr< ::grpc::ClientAsyncWriter< ::com::ibm::cloudland::scripts::FileChunk>> PrepareAsyncTransmit(::grpc::ClientContext* context, ::com::ibm::cloudland::scripts::TransmitAck* response, ::grpc::CompletionQueue* cq) {
      return std::unique_ptr< ::grpc::ClientAsyncWriter< ::com::ibm::cloudland::scripts::FileChunk>>(PrepareAsyncTransmitRaw(context, response, cq));
    }
    class experimental_async final :
      public StubInterface::experimental_async_interface {
     public:
      void Execute(::grpc::ClientContext* context, const ::com::ibm::cloudland::scripts::ExecuteRequest* request, ::com::ibm::cloudland::scripts::ExecuteReply* response, std::function<void(::grpc::Status)>) override;
      void Execute(::grpc::ClientContext* context, const ::grpc::ByteBuffer* request, ::com::ibm::cloudland::scripts::ExecuteReply* response, std::function<void(::grpc::Status)>) override;
      #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
      void Execute(::grpc::ClientContext* context, const ::com::ibm::cloudland::scripts::ExecuteRequest* request, ::com::ibm::cloudland::scripts::ExecuteReply* response, ::grpc::ClientUnaryReactor* reactor) override;
      #else
      void Execute(::grpc::ClientContext* context, const ::com::ibm::cloudland::scripts::ExecuteRequest* request, ::com::ibm::cloudland::scripts::ExecuteReply* response, ::grpc::experimental::ClientUnaryReactor* reactor) override;
      #endif
      #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
      void Execute(::grpc::ClientContext* context, const ::grpc::ByteBuffer* request, ::com::ibm::cloudland::scripts::ExecuteReply* response, ::grpc::ClientUnaryReactor* reactor) override;
      #else
      void Execute(::grpc::ClientContext* context, const ::grpc::ByteBuffer* request, ::com::ibm::cloudland::scripts::ExecuteReply* response, ::grpc::experimental::ClientUnaryReactor* reactor) override;
      #endif
      #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
      void Transmit(::grpc::ClientContext* context, ::com::ibm::cloudland::scripts::TransmitAck* response, ::grpc::ClientWriteReactor< ::com::ibm::cloudland::scripts::FileChunk>* reactor) override;
      #else
      void Transmit(::grpc::ClientContext* context, ::com::ibm::cloudland::scripts::TransmitAck* response, ::grpc::experimental::ClientWriteReactor< ::com::ibm::cloudland::scripts::FileChunk>* reactor) override;
      #endif
     private:
      friend class Stub;
      explicit experimental_async(Stub* stub): stub_(stub) { }
      Stub* stub() { return stub_; }
      Stub* stub_;
    };
    class experimental_async_interface* experimental_async() override { return &async_stub_; }

   private:
    std::shared_ptr< ::grpc::ChannelInterface> channel_;
    class experimental_async async_stub_{this};
    ::grpc::ClientAsyncResponseReader< ::com::ibm::cloudland::scripts::ExecuteReply>* AsyncExecuteRaw(::grpc::ClientContext* context, const ::com::ibm::cloudland::scripts::ExecuteRequest& request, ::grpc::CompletionQueue* cq) override;
    ::grpc::ClientAsyncResponseReader< ::com::ibm::cloudland::scripts::ExecuteReply>* PrepareAsyncExecuteRaw(::grpc::ClientContext* context, const ::com::ibm::cloudland::scripts::ExecuteRequest& request, ::grpc::CompletionQueue* cq) override;
    ::grpc::ClientWriter< ::com::ibm::cloudland::scripts::FileChunk>* TransmitRaw(::grpc::ClientContext* context, ::com::ibm::cloudland::scripts::TransmitAck* response) override;
    ::grpc::ClientAsyncWriter< ::com::ibm::cloudland::scripts::FileChunk>* AsyncTransmitRaw(::grpc::ClientContext* context, ::com::ibm::cloudland::scripts::TransmitAck* response, ::grpc::CompletionQueue* cq, void* tag) override;
    ::grpc::ClientAsyncWriter< ::com::ibm::cloudland::scripts::FileChunk>* PrepareAsyncTransmitRaw(::grpc::ClientContext* context, ::com::ibm::cloudland::scripts::TransmitAck* response, ::grpc::CompletionQueue* cq) override;
    const ::grpc::internal::RpcMethod rpcmethod_Execute_;
    const ::grpc::internal::RpcMethod rpcmethod_Transmit_;
  };
  static std::unique_ptr<Stub> NewStub(const std::shared_ptr< ::grpc::ChannelInterface>& channel, const ::grpc::StubOptions& options = ::grpc::StubOptions());

  class Service : public ::grpc::Service {
   public:
    Service();
    virtual ~Service();
    virtual ::grpc::Status Execute(::grpc::ServerContext* context, const ::com::ibm::cloudland::scripts::ExecuteRequest* request, ::com::ibm::cloudland::scripts::ExecuteReply* response);
    virtual ::grpc::Status Transmit(::grpc::ServerContext* context, ::grpc::ServerReader< ::com::ibm::cloudland::scripts::FileChunk>* reader, ::com::ibm::cloudland::scripts::TransmitAck* response);
  };
  template <class BaseClass>
  class WithAsyncMethod_Execute : public BaseClass {
   private:
    void BaseClassMustBeDerivedFromService(const Service* /*service*/) {}
   public:
    WithAsyncMethod_Execute() {
      ::grpc::Service::MarkMethodAsync(0);
    }
    ~WithAsyncMethod_Execute() override {
      BaseClassMustBeDerivedFromService(this);
    }
    // disable synchronous version of this method
    ::grpc::Status Execute(::grpc::ServerContext* /*context*/, const ::com::ibm::cloudland::scripts::ExecuteRequest* /*request*/, ::com::ibm::cloudland::scripts::ExecuteReply* /*response*/) override {
      abort();
      return ::grpc::Status(::grpc::StatusCode::UNIMPLEMENTED, "");
    }
    void RequestExecute(::grpc::ServerContext* context, ::com::ibm::cloudland::scripts::ExecuteRequest* request, ::grpc::ServerAsyncResponseWriter< ::com::ibm::cloudland::scripts::ExecuteReply>* response, ::grpc::CompletionQueue* new_call_cq, ::grpc::ServerCompletionQueue* notification_cq, void *tag) {
      ::grpc::Service::RequestAsyncUnary(0, context, request, response, new_call_cq, notification_cq, tag);
    }
  };
  template <class BaseClass>
  class WithAsyncMethod_Transmit : public BaseClass {
   private:
    void BaseClassMustBeDerivedFromService(const Service* /*service*/) {}
   public:
    WithAsyncMethod_Transmit() {
      ::grpc::Service::MarkMethodAsync(1);
    }
    ~WithAsyncMethod_Transmit() override {
      BaseClassMustBeDerivedFromService(this);
    }
    // disable synchronous version of this method
    ::grpc::Status Transmit(::grpc::ServerContext* /*context*/, ::grpc::ServerReader< ::com::ibm::cloudland::scripts::FileChunk>* /*reader*/, ::com::ibm::cloudland::scripts::TransmitAck* /*response*/) override {
      abort();
      return ::grpc::Status(::grpc::StatusCode::UNIMPLEMENTED, "");
    }
    void RequestTransmit(::grpc::ServerContext* context, ::grpc::ServerAsyncReader< ::com::ibm::cloudland::scripts::TransmitAck, ::com::ibm::cloudland::scripts::FileChunk>* reader, ::grpc::CompletionQueue* new_call_cq, ::grpc::ServerCompletionQueue* notification_cq, void *tag) {
      ::grpc::Service::RequestAsyncClientStreaming(1, context, reader, new_call_cq, notification_cq, tag);
    }
  };
  typedef WithAsyncMethod_Execute<WithAsyncMethod_Transmit<Service > > AsyncService;
  template <class BaseClass>
  class ExperimentalWithCallbackMethod_Execute : public BaseClass {
   private:
    void BaseClassMustBeDerivedFromService(const Service* /*service*/) {}
   public:
    ExperimentalWithCallbackMethod_Execute() {
    #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
      ::grpc::Service::
    #else
      ::grpc::Service::experimental().
    #endif
        MarkMethodCallback(0,
          new ::grpc_impl::internal::CallbackUnaryHandler< ::com::ibm::cloudland::scripts::ExecuteRequest, ::com::ibm::cloudland::scripts::ExecuteReply>(
            [this](
    #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
                   ::grpc::CallbackServerContext*
    #else
                   ::grpc::experimental::CallbackServerContext*
    #endif
                     context, const ::com::ibm::cloudland::scripts::ExecuteRequest* request, ::com::ibm::cloudland::scripts::ExecuteReply* response) { return this->Execute(context, request, response); }));}
    void SetMessageAllocatorFor_Execute(
        ::grpc::experimental::MessageAllocator< ::com::ibm::cloudland::scripts::ExecuteRequest, ::com::ibm::cloudland::scripts::ExecuteReply>* allocator) {
    #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
      ::grpc::internal::MethodHandler* const handler = ::grpc::Service::GetHandler(0);
    #else
      ::grpc::internal::MethodHandler* const handler = ::grpc::Service::experimental().GetHandler(0);
    #endif
      static_cast<::grpc_impl::internal::CallbackUnaryHandler< ::com::ibm::cloudland::scripts::ExecuteRequest, ::com::ibm::cloudland::scripts::ExecuteReply>*>(handler)
              ->SetMessageAllocator(allocator);
    }
    ~ExperimentalWithCallbackMethod_Execute() override {
      BaseClassMustBeDerivedFromService(this);
    }
    // disable synchronous version of this method
    ::grpc::Status Execute(::grpc::ServerContext* /*context*/, const ::com::ibm::cloudland::scripts::ExecuteRequest* /*request*/, ::com::ibm::cloudland::scripts::ExecuteReply* /*response*/) override {
      abort();
      return ::grpc::Status(::grpc::StatusCode::UNIMPLEMENTED, "");
    }
    #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
    virtual ::grpc::ServerUnaryReactor* Execute(
      ::grpc::CallbackServerContext* /*context*/, const ::com::ibm::cloudland::scripts::ExecuteRequest* /*request*/, ::com::ibm::cloudland::scripts::ExecuteReply* /*response*/)
    #else
    virtual ::grpc::experimental::ServerUnaryReactor* Execute(
      ::grpc::experimental::CallbackServerContext* /*context*/, const ::com::ibm::cloudland::scripts::ExecuteRequest* /*request*/, ::com::ibm::cloudland::scripts::ExecuteReply* /*response*/)
    #endif
      { return nullptr; }
  };
  template <class BaseClass>
  class ExperimentalWithCallbackMethod_Transmit : public BaseClass {
   private:
    void BaseClassMustBeDerivedFromService(const Service* /*service*/) {}
   public:
    ExperimentalWithCallbackMethod_Transmit() {
    #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
      ::grpc::Service::
    #else
      ::grpc::Service::experimental().
    #endif
        MarkMethodCallback(1,
          new ::grpc_impl::internal::CallbackClientStreamingHandler< ::com::ibm::cloudland::scripts::FileChunk, ::com::ibm::cloudland::scripts::TransmitAck>(
            [this](
    #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
                   ::grpc::CallbackServerContext*
    #else
                   ::grpc::experimental::CallbackServerContext*
    #endif
                     context, ::com::ibm::cloudland::scripts::TransmitAck* response) { return this->Transmit(context, response); }));
    }
    ~ExperimentalWithCallbackMethod_Transmit() override {
      BaseClassMustBeDerivedFromService(this);
    }
    // disable synchronous version of this method
    ::grpc::Status Transmit(::grpc::ServerContext* /*context*/, ::grpc::ServerReader< ::com::ibm::cloudland::scripts::FileChunk>* /*reader*/, ::com::ibm::cloudland::scripts::TransmitAck* /*response*/) override {
      abort();
      return ::grpc::Status(::grpc::StatusCode::UNIMPLEMENTED, "");
    }
    #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
    virtual ::grpc::ServerReadReactor< ::com::ibm::cloudland::scripts::FileChunk>* Transmit(
      ::grpc::CallbackServerContext* /*context*/, ::com::ibm::cloudland::scripts::TransmitAck* /*response*/)
    #else
    virtual ::grpc::experimental::ServerReadReactor< ::com::ibm::cloudland::scripts::FileChunk>* Transmit(
      ::grpc::experimental::CallbackServerContext* /*context*/, ::com::ibm::cloudland::scripts::TransmitAck* /*response*/)
    #endif
      { return nullptr; }
  };
  #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
  typedef ExperimentalWithCallbackMethod_Execute<ExperimentalWithCallbackMethod_Transmit<Service > > CallbackService;
  #endif

  typedef ExperimentalWithCallbackMethod_Execute<ExperimentalWithCallbackMethod_Transmit<Service > > ExperimentalCallbackService;
  template <class BaseClass>
  class WithGenericMethod_Execute : public BaseClass {
   private:
    void BaseClassMustBeDerivedFromService(const Service* /*service*/) {}
   public:
    WithGenericMethod_Execute() {
      ::grpc::Service::MarkMethodGeneric(0);
    }
    ~WithGenericMethod_Execute() override {
      BaseClassMustBeDerivedFromService(this);
    }
    // disable synchronous version of this method
    ::grpc::Status Execute(::grpc::ServerContext* /*context*/, const ::com::ibm::cloudland::scripts::ExecuteRequest* /*request*/, ::com::ibm::cloudland::scripts::ExecuteReply* /*response*/) override {
      abort();
      return ::grpc::Status(::grpc::StatusCode::UNIMPLEMENTED, "");
    }
  };
  template <class BaseClass>
  class WithGenericMethod_Transmit : public BaseClass {
   private:
    void BaseClassMustBeDerivedFromService(const Service* /*service*/) {}
   public:
    WithGenericMethod_Transmit() {
      ::grpc::Service::MarkMethodGeneric(1);
    }
    ~WithGenericMethod_Transmit() override {
      BaseClassMustBeDerivedFromService(this);
    }
    // disable synchronous version of this method
    ::grpc::Status Transmit(::grpc::ServerContext* /*context*/, ::grpc::ServerReader< ::com::ibm::cloudland::scripts::FileChunk>* /*reader*/, ::com::ibm::cloudland::scripts::TransmitAck* /*response*/) override {
      abort();
      return ::grpc::Status(::grpc::StatusCode::UNIMPLEMENTED, "");
    }
  };
  template <class BaseClass>
  class WithRawMethod_Execute : public BaseClass {
   private:
    void BaseClassMustBeDerivedFromService(const Service* /*service*/) {}
   public:
    WithRawMethod_Execute() {
      ::grpc::Service::MarkMethodRaw(0);
    }
    ~WithRawMethod_Execute() override {
      BaseClassMustBeDerivedFromService(this);
    }
    // disable synchronous version of this method
    ::grpc::Status Execute(::grpc::ServerContext* /*context*/, const ::com::ibm::cloudland::scripts::ExecuteRequest* /*request*/, ::com::ibm::cloudland::scripts::ExecuteReply* /*response*/) override {
      abort();
      return ::grpc::Status(::grpc::StatusCode::UNIMPLEMENTED, "");
    }
    void RequestExecute(::grpc::ServerContext* context, ::grpc::ByteBuffer* request, ::grpc::ServerAsyncResponseWriter< ::grpc::ByteBuffer>* response, ::grpc::CompletionQueue* new_call_cq, ::grpc::ServerCompletionQueue* notification_cq, void *tag) {
      ::grpc::Service::RequestAsyncUnary(0, context, request, response, new_call_cq, notification_cq, tag);
    }
  };
  template <class BaseClass>
  class WithRawMethod_Transmit : public BaseClass {
   private:
    void BaseClassMustBeDerivedFromService(const Service* /*service*/) {}
   public:
    WithRawMethod_Transmit() {
      ::grpc::Service::MarkMethodRaw(1);
    }
    ~WithRawMethod_Transmit() override {
      BaseClassMustBeDerivedFromService(this);
    }
    // disable synchronous version of this method
    ::grpc::Status Transmit(::grpc::ServerContext* /*context*/, ::grpc::ServerReader< ::com::ibm::cloudland::scripts::FileChunk>* /*reader*/, ::com::ibm::cloudland::scripts::TransmitAck* /*response*/) override {
      abort();
      return ::grpc::Status(::grpc::StatusCode::UNIMPLEMENTED, "");
    }
    void RequestTransmit(::grpc::ServerContext* context, ::grpc::ServerAsyncReader< ::grpc::ByteBuffer, ::grpc::ByteBuffer>* reader, ::grpc::CompletionQueue* new_call_cq, ::grpc::ServerCompletionQueue* notification_cq, void *tag) {
      ::grpc::Service::RequestAsyncClientStreaming(1, context, reader, new_call_cq, notification_cq, tag);
    }
  };
  template <class BaseClass>
  class ExperimentalWithRawCallbackMethod_Execute : public BaseClass {
   private:
    void BaseClassMustBeDerivedFromService(const Service* /*service*/) {}
   public:
    ExperimentalWithRawCallbackMethod_Execute() {
    #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
      ::grpc::Service::
    #else
      ::grpc::Service::experimental().
    #endif
        MarkMethodRawCallback(0,
          new ::grpc_impl::internal::CallbackUnaryHandler< ::grpc::ByteBuffer, ::grpc::ByteBuffer>(
            [this](
    #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
                   ::grpc::CallbackServerContext*
    #else
                   ::grpc::experimental::CallbackServerContext*
    #endif
                     context, const ::grpc::ByteBuffer* request, ::grpc::ByteBuffer* response) { return this->Execute(context, request, response); }));
    }
    ~ExperimentalWithRawCallbackMethod_Execute() override {
      BaseClassMustBeDerivedFromService(this);
    }
    // disable synchronous version of this method
    ::grpc::Status Execute(::grpc::ServerContext* /*context*/, const ::com::ibm::cloudland::scripts::ExecuteRequest* /*request*/, ::com::ibm::cloudland::scripts::ExecuteReply* /*response*/) override {
      abort();
      return ::grpc::Status(::grpc::StatusCode::UNIMPLEMENTED, "");
    }
    #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
    virtual ::grpc::ServerUnaryReactor* Execute(
      ::grpc::CallbackServerContext* /*context*/, const ::grpc::ByteBuffer* /*request*/, ::grpc::ByteBuffer* /*response*/)
    #else
    virtual ::grpc::experimental::ServerUnaryReactor* Execute(
      ::grpc::experimental::CallbackServerContext* /*context*/, const ::grpc::ByteBuffer* /*request*/, ::grpc::ByteBuffer* /*response*/)
    #endif
      { return nullptr; }
  };
  template <class BaseClass>
  class ExperimentalWithRawCallbackMethod_Transmit : public BaseClass {
   private:
    void BaseClassMustBeDerivedFromService(const Service* /*service*/) {}
   public:
    ExperimentalWithRawCallbackMethod_Transmit() {
    #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
      ::grpc::Service::
    #else
      ::grpc::Service::experimental().
    #endif
        MarkMethodRawCallback(1,
          new ::grpc_impl::internal::CallbackClientStreamingHandler< ::grpc::ByteBuffer, ::grpc::ByteBuffer>(
            [this](
    #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
                   ::grpc::CallbackServerContext*
    #else
                   ::grpc::experimental::CallbackServerContext*
    #endif
                     context, ::grpc::ByteBuffer* response) { return this->Transmit(context, response); }));
    }
    ~ExperimentalWithRawCallbackMethod_Transmit() override {
      BaseClassMustBeDerivedFromService(this);
    }
    // disable synchronous version of this method
    ::grpc::Status Transmit(::grpc::ServerContext* /*context*/, ::grpc::ServerReader< ::com::ibm::cloudland::scripts::FileChunk>* /*reader*/, ::com::ibm::cloudland::scripts::TransmitAck* /*response*/) override {
      abort();
      return ::grpc::Status(::grpc::StatusCode::UNIMPLEMENTED, "");
    }
    #ifdef GRPC_CALLBACK_API_NONEXPERIMENTAL
    virtual ::grpc::ServerReadReactor< ::grpc::ByteBuffer>* Transmit(
      ::grpc::CallbackServerContext* /*context*/, ::grpc::ByteBuffer* /*response*/)
    #else
    virtual ::grpc::experimental::ServerReadReactor< ::grpc::ByteBuffer>* Transmit(
      ::grpc::experimental::CallbackServerContext* /*context*/, ::grpc::ByteBuffer* /*response*/)
    #endif
      { return nullptr; }
  };
  template <class BaseClass>
  class WithStreamedUnaryMethod_Execute : public BaseClass {
   private:
    void BaseClassMustBeDerivedFromService(const Service* /*service*/) {}
   public:
    WithStreamedUnaryMethod_Execute() {
      ::grpc::Service::MarkMethodStreamed(0,
        new ::grpc::internal::StreamedUnaryHandler< ::com::ibm::cloudland::scripts::ExecuteRequest, ::com::ibm::cloudland::scripts::ExecuteReply>(std::bind(&WithStreamedUnaryMethod_Execute<BaseClass>::StreamedExecute, this, std::placeholders::_1, std::placeholders::_2)));
    }
    ~WithStreamedUnaryMethod_Execute() override {
      BaseClassMustBeDerivedFromService(this);
    }
    // disable regular version of this method
    ::grpc::Status Execute(::grpc::ServerContext* /*context*/, const ::com::ibm::cloudland::scripts::ExecuteRequest* /*request*/, ::com::ibm::cloudland::scripts::ExecuteReply* /*response*/) override {
      abort();
      return ::grpc::Status(::grpc::StatusCode::UNIMPLEMENTED, "");
    }
    // replace default version of method with streamed unary
    virtual ::grpc::Status StreamedExecute(::grpc::ServerContext* context, ::grpc::ServerUnaryStreamer< ::com::ibm::cloudland::scripts::ExecuteRequest,::com::ibm::cloudland::scripts::ExecuteReply>* server_unary_streamer) = 0;
  };
  typedef WithStreamedUnaryMethod_Execute<Service > StreamedUnaryService;
  typedef Service SplitStreamedService;
  typedef WithStreamedUnaryMethod_Execute<Service > StreamedService;
};

}  // namespace scripts
}  // namespace cloudland
}  // namespace ibm
}  // namespace com


#endif  // GRPC_remotexec_2eproto__INCLUDED
