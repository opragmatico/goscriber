// Package webhook sends every finished transcription to the user's URL with
// an HTTP POST.
//
// Each POST is a River job (github.com/riverqueue/river) in a SQLite
// database of its own, so a transcription that cannot be sent yet waits for
// its retry, across launches if need be. Delivery is at least once: an
// attempt cut short after the server got it is made again. So every attempt
// carries the delivery's ID in the webhook-id header, as Standard Webhooks
// names it: a random UUID made when the transcription is queued, for the
// receiver to drop repeats by. The body's transcription_id, the history
// entry's ID, says which transcription it is.
//
// Network errors, timeouts, 408, 429 and 5xx answers are retried with
// River's backoff, attempt⁴ seconds, for MaxAttempts attempts in all, about
// a day. Any other answer outside 2xx refuses the transcription for good.
// Either way, a transcription that will not be sent is reported once, to
// Options.OnFailure.
package webhook
