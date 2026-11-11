// Wrappers around the fallible nemo_speech_asr_* calls the binding makes.
//
// nemo_speech_asr_last_error() is thread-local, and Go may run a goroutine on
// another OS thread between two CGo calls. So each wrapper makes its call and,
// when it fails, copies the error message in the same C call. The copy is
// malloc'd; the caller frees it.

#ifndef GOSCRIBER_NEMO_SHIM_H
#define GOSCRIBER_NEMO_SHIM_H

#include <stdlib.h>
#include <string.h>

#if defined(__APPLE__)
#include <malloc/malloc.h>
#elif defined(__GLIBC__)
#include <malloc.h>
#endif

#include <nemo_speech/asr.h>

static inline nemo_speech_asr_status gs_status(nemo_speech_asr_status status, char** err) {
    if (status != NEMO_SPEECH_ASR_OK && err != NULL) {
        const char* msg = nemo_speech_asr_last_error();
        *err = strdup(msg != NULL ? msg : "");
    }
    return status;
}

static inline nemo_speech_asr_status gs_create(
    const nemo_speech_asr_recognizer_config* cfg, nemo_speech_asr_recognizer** out, char** err) {
    return gs_status(nemo_speech_asr_create(cfg, out), err);
}

static inline nemo_speech_asr_status gs_recognize_f32(
    nemo_speech_asr_recognizer* recognizer, const nemo_speech_asr_recognition_options* options,
    const float* samples, size_t n_samples, int32_t sample_rate, nemo_speech_asr_result** out,
    char** err) {
    return gs_status(
        nemo_speech_asr_recognize_f32(recognizer, options, samples, n_samples, sample_rate, out),
        err);
}

// gs_release_free_memory hands memory the C allocator holds but no longer
// uses back to the OS. A recognizer frees hundreds of megabytes when it is
// destroyed; without this the allocator keeps much of it resident for reuse.
static inline void gs_release_free_memory(void) {
#if defined(__APPLE__)
    malloc_zone_pressure_relief(NULL, 0);
#elif defined(__GLIBC__)
    malloc_trim(0);
#endif
}

#endif  // GOSCRIBER_NEMO_SHIM_H
