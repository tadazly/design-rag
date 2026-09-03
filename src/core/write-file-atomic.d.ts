declare module "write-file-atomic" {
  interface WriteFileAtomicOptions {
    encoding?: BufferEncoding;
    mode?: number;
    chown?: { uid: number; gid: number };
    fsync?: boolean;
    tmpfileCreated?: (tmpfile: string) => void;
  }

  function writeFileAtomic(
    filename: string,
    data: string | Uint8Array,
    options?: WriteFileAtomicOptions,
  ): Promise<void>;

  export default writeFileAtomic;
}
