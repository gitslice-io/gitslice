// The API methods that stream: async generators taking a signal, where every
// other method is a promise. The lazy client (./lazyClient) must know which
// are which before the client's code, with the service descriptors that say so,
// has been fetched; ./client.test.ts checks this list against the descriptors,
// so a new server-streaming RPC that is not named here fails the build.
export const SERVER_STREAMING_METHODS: ReadonlySet<string> = new Set([
  "checkout",
  "importGitRepositoryStream",
  "readBlobStream",
  "streamCheckRun",
  "streamConversation"
]);
