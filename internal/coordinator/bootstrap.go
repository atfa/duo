package coordinator

// v0.2 intentionally keeps bootstrap policy in the Pi system prompt instead of
// forcing a coordinator-side wake-up. The human talks only to Austin; Austin is
// instructed to understand the task first, then use duo_send to wake Tony and
// request an independent proposal. This preserves peer symmetry after startup
// and avoids inventing a second hidden copy of the user's prompt in Duo Core.
