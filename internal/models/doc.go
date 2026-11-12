// Package models holds the embedded catalog of supported models and downloads,
// verifies and deletes model files on the user's request.
//
// The catalog (catalog.json) carries metadata only: each model's Hugging Face
// repository at a pinned revision, and the name, size and SHA-256 of each
// quantisation's file. No weights are embedded, and nothing downloads on its
// own: every download starts from an explicit user action through
// Manager.Download. The Silero VAD companion comes with the first ASR model
// download.
//
// A Manager keeps the files in one directory (config.Paths.ModelsDir). It
// also finds matching files already in the Hugging Face cache and lists them
// as ready once their checksum is verified, so a model another tool
// downloaded is not fetched twice. It never selects a model: choosing one is
// the user's call.
//
// Downloads resume from a ".part" file with HTTP Range requests, retry
// transient failures with backoff, check the free space first, verify the
// SHA-256 and only then move the file into place.
package models
