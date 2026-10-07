// Native Triton custom batch membership only; scheduling and waiting remain native.
#include "triton/core/tritonbackend.h"

#include <cstdio>
#include <cstring>
#include <ctime>
#include <new>
#include <sys/time.h>

namespace {
constexpr size_t kIdentityBytes = 512;
constexpr char kGuard[] = "same-workload-uid-v1";
struct Batch { char uid[kIdentityBytes + 1]{}; };

TRITONSERVER_Error* Invalid(const char* message)
{
  return TRITONSERVER_ErrorNew(TRITONSERVER_ERROR_INVALID_ARG, message);
}

bool ValidIdentity(const char* value)
{
  if (value == nullptr || value[0] == '\0' || strnlen(value, kIdentityBytes + 1) > kIdentityBytes) return false;
  for (const unsigned char* c = reinterpret_cast<const unsigned char*>(value); *c; ++c) {
    if (*c < 32 || *c == 127) return false;
  }
  return true;
}

// Caller provides space for twice the bounded ID plus quotes and terminator.
char* Quote(char* out, const char* value)
{
  *out++ = '"';
  for (const char* c = value; *c; ++c) {
    if (*c == '"' || *c == '\\') *out++ = '\\';
    *out++ = *c;
  }
  *out++ = '"';
  *out = '\0';
  return out;
}

void Queued(const char* id, const char* uid, const char* definition)
{
  char escaped_id[kIdentityBytes * 2 + 3];
  char escaped_uid[kIdentityBytes * 2 + 3];
  char escaped_definition[kIdentityBytes * 2 + 3];
  Quote(escaped_id, id); Quote(escaped_uid, uid); Quote(escaped_definition, definition);
  timeval now{}; gettimeofday(&now, nullptr);
  tm utc{}; gmtime_r(&now.tv_sec, &utc);
  char date[32]; strftime(date, sizeof(date), "%Y-%m-%dT%H:%M:%S", &utc);
  char line[4096];
  const int size = snprintf(line, sizeof(line),
      "CHILL_PROGRESS {\"requestID\":%s,\"workloadUID\":%s,\"definition\":%s,\"phase\":\"queued\",\"at\":\"%s.%06ldZ\"}\n",
      escaped_id, escaped_uid, escaped_definition, date, static_cast<long>(now.tv_usec));
  // One write under stdio's lock prevents callback records interleaving.
  if (size > 0 && static_cast<size_t>(size) < sizeof(line)) {
    flockfile(stdout); fwrite(line, 1, size, stdout); fflush(stdout); funlockfile(stdout);
  }
}
}  // namespace

extern "C" {
TRITONSERVER_Error* TRITONBACKEND_ModelBatcherInitialize(
    TRITONBACKEND_Batcher** batcher, TRITONBACKEND_Model*)
{
  uint32_t major = 0, minor = 0;
  if (auto* error = TRITONBACKEND_ApiVersion(&major, &minor)) return error;
  if (major != TRITONBACKEND_API_VERSION_MAJOR || minor < TRITONBACKEND_API_VERSION_MINOR) {
    return TRITONSERVER_ErrorNew(TRITONSERVER_ERROR_UNSUPPORTED, "incompatible native batch callback API");
  }
  *batcher = reinterpret_cast<TRITONBACKEND_Batcher*>(const_cast<char*>(kGuard));
  return nullptr;
}

TRITONSERVER_Error* TRITONBACKEND_ModelBatcherFinalize(TRITONBACKEND_Batcher*) { return nullptr; }

TRITONSERVER_Error* TRITONBACKEND_ModelBatchInitialize(const TRITONBACKEND_Batcher*, void** userp)
{
  *userp = new (std::nothrow) Batch;
  return *userp ? nullptr : TRITONSERVER_ErrorNew(TRITONSERVER_ERROR_INTERNAL, "native batch state allocation failed");
}

TRITONSERVER_Error* TRITONBACKEND_ModelBatchFinalize(void* userp)
{
  delete static_cast<Batch*>(userp);
  return nullptr;
}

TRITONSERVER_Error* TRITONBACKEND_ModelBatchIncludeRequest(
    TRITONBACKEND_Request* request, void* userp, bool* should_include)
{
  *should_include = false;
  const char* id = nullptr;
  if (auto* error = TRITONBACKEND_RequestId(request, &id)) return error;
  const char* uid = nullptr;
  const char* definition = nullptr;
  uint32_t count = 0;
  if (auto* error = TRITONBACKEND_RequestParameterCount(request, &count)) return error;
  if (count > 16) return Invalid("too many native request parameters");
  for (uint32_t index = 0; index < count; ++index) {
    const char* key = nullptr; const void* value = nullptr;
    TRITONSERVER_ParameterType type;
    if (auto* error = TRITONBACKEND_RequestParameter(request, index, &key, &type, &value)) return error;
    if (strcmp(key, "chill_workload_uid") == 0 || strcmp(key, "chill_definition") == 0) {
      if (type != TRITONSERVER_PARAMETER_STRING) return Invalid("native UID/definition must be strings");
      const char** destination = strcmp(key, "chill_workload_uid") == 0 ? &uid : &definition;
      if (*destination != nullptr) return Invalid("duplicate native identity parameter");
      *destination = static_cast<const char*>(value);
    }
  }
  if (!ValidIdentity(id) || !ValidIdentity(uid) || !ValidIdentity(definition)) return Invalid("bounded native request identities required");
  Queued(id, uid, definition);
  auto* batch = static_cast<Batch*>(userp);
  if (batch->uid[0] == '\0') memcpy(batch->uid, uid, strlen(uid) + 1);
  *should_include = strcmp(batch->uid, uid) == 0;
  return nullptr;
}
}  // extern "C"
