// Golden vectors shared with the Go client (internal/remote/crypto_test.go).
// They are test values and must never ship as a pairing.

export const VECTOR = {
  key: "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8",
  room: "QEFCQ0RFRkdISUpLTE1OTw",
  proof: "lY4LXUExKI_Iu9P2mf-aQ6kQ81cqaTdyp9tTFSSOT3g",
  iv: "AQIDBAUGBwgJCgsM",
  seq: 1,
  kind: "state",
  plaintext: '{"v":1,"t":"state","session":{"id":"demo","mode":"step","phase":"execute"}}',
  ciphertext:
    "tH3sLK7dMllmfr7Je85_wAfDvVr3nHftuuvP8In30ERgsENojPcqVHcLqhX7Ubz97D87hcvKwIcWfB-B6GpXz_V59p5MWaXd_2_2yEF1L_glKE83WgvZ35jUNA",
  mac: "z3lbGYzVxeRridRNlSahRvSQdrkPOnSAtC-BGqRVIH4",
  macTask: "z3lbGYzVxeRridRNlSahRvSQdrkPOnSAtC-BGqRVIH4",
};
