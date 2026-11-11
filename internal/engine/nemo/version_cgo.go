//go:build cgo && nemo

package nemo

/*
#cgo pkg-config: nemo-speech-asr
#include <nemo_speech/asr.h>
*/
import "C"

// Version returns the version of the NeMo-Speech.cpp ASR runtime the process
// loaded, such as "0.3.0".
func Version() string {
	return parseVersion(C.GoString(C.nemo_speech_asr_version()))
}
