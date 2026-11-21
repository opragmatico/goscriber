//go:build cgo && nemo

package nemo

/*
#cgo pkg-config: nemo-speech-asr
// dl_iterate_phdr, before the first system header.
#if defined(__linux__) && !defined(_GNU_SOURCE)
#define _GNU_SOURCE
#endif
#include <string.h>

// The SDK links ggml and its backends as shared libraries the binary loads
// at startup (no GGML_BACKEND_DL), so the backend library loaded in the
// process names the SDK's GPU backend: libggml-metal, libggml-vulkan or
// libggml-cuda, or none beside libggml-cpu.
enum { GS_GGML_NONE = 0, GS_GGML_CPU = 1, GS_GGML_METAL = 2, GS_GGML_VULKAN = 3, GS_GGML_CUDA = 4 };

static int gs_ggml_backend_of(const char* path) {
	if (path == NULL) return GS_GGML_NONE;
	if (strstr(path, "libggml-metal") != NULL) return GS_GGML_METAL;
	if (strstr(path, "libggml-vulkan") != NULL) return GS_GGML_VULKAN;
	if (strstr(path, "libggml-cuda") != NULL) return GS_GGML_CUDA;
	if (strstr(path, "libggml-cpu") != NULL) return GS_GGML_CPU;
	return GS_GGML_NONE;
}

#if defined(__APPLE__)
#include <mach-o/dyld.h>

static int gs_loaded_ggml(void) {
	int found = GS_GGML_NONE;
	for (uint32_t i = 0; i < _dyld_image_count(); i++) {
		int b = gs_ggml_backend_of(_dyld_get_image_name(i));
		if (b > found) found = b;
	}
	return found;
}
#elif defined(__linux__) || defined(__FreeBSD__)
#include <link.h>

static int gs_phdr(struct dl_phdr_info* info, size_t size, void* data) {
	int* found = (int*)data;
	int b = gs_ggml_backend_of(info->dlpi_name);
	if (b > *found) *found = b;
	return 0;
}

static int gs_loaded_ggml(void) {
	int found = GS_GGML_NONE;
	dl_iterate_phdr(gs_phdr, &found);
	return found;
}
#else
static int gs_loaded_ggml(void) { return GS_GGML_NONE; }
#endif

// The backend scripts/build-nemo-speech.sh wrote into the pkg-config file
// as GOSCRIBER_NEMO_GPU_<NAME>, for when the loaded libraries cannot be
// told apart. An SDK installed before it did is the default preset's:
// Metal on macOS, none on Linux. Go's build cache does not notice a new
// pkg-config file, so after a change of preset this may be stale until a
// clean build; the loaded libraries never are.
static const char* gs_built_gpu_backend(void) {
#if defined(GOSCRIBER_NEMO_GPU_METAL)
	return "Metal";
#elif defined(GOSCRIBER_NEMO_GPU_VULKAN)
	return "Vulkan";
#elif defined(GOSCRIBER_NEMO_GPU_CUDA)
	return "CUDA";
#elif defined(GOSCRIBER_NEMO_GPU_NONE)
	return "";
#elif defined(__APPLE__)
	return "Metal";
#else
	return "";
#endif
}
*/
import "C"

// GPUBackend names the GPU backend of the NeMo-Speech.cpp SDK the process
// runs on: "Metal" (metal-asr), "Vulkan" (vulkan-asr) or "CUDA" (cuda-asr),
// or "" for a CPU-only SDK (cpu-asr), which loads every model on the CPU.
// It goes by the ggml backend library loaded in the process, and by the
// SDK's pkg-config file when it cannot tell.
func GPUBackend() string {
	switch C.gs_loaded_ggml() {
	case C.GS_GGML_METAL:
		return "Metal"
	case C.GS_GGML_VULKAN:
		return "Vulkan"
	case C.GS_GGML_CUDA:
		return "CUDA"
	case C.GS_GGML_CPU:
		return ""
	default:
		return C.GoString(C.gs_built_gpu_backend())
	}
}
