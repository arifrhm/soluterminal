// The browser side of the private relay channel
// (docs/DESIGN-trusted-devices-and-private-relay.md, step 2): an end-to-end
// encrypted, authenticated tunnel to an approved computer's daemon, carried
// by the cloud relay as one WebSocket.
//
//   handshake.ts   hello/reply exchange and key derivation
//   records.ts     AES-256-GCM records in counter order
//   mux.ts         stream frames over the records
//   channel.ts     one channel: WebSocket + handshake + records + mux
//   http.ts        HTTP/1.1 over one stream, returning a streaming Response
//   websocket.ts   RFC 6455 client over one stream, shaped like WebSocket
//   eventStream.ts text/event-stream reader shaped like EventSource
//   transport.ts   when the app uses the channel (strictly), one per target

export {
  closePrivateRelayChannels,
  configurePrivateRelay,
  openPrivateRelayEventStream,
  openPrivateRelayWebSocket,
  PRIVATE_RELAY_IDENTITY_EVENT,
  PRIVATE_RELAY_IDENTITY_MESSAGE,
  PRIVATE_RELAY_STATUS_EVENT,
  PrivateRelayApprovalNeeded,
  privateRelayUnreachableTargets,
  privateRelayCandidate,
  privateRelayEnabled,
  PrivateRelayIdentityRefused,
  privateRelayTarget,
  relayFetch,
  setPrivateRelayEnabled,
} from './transport';
