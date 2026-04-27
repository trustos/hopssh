/// <reference types="svelte" />
/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_HOPSSH_API?: string;
  readonly VITE_HOPSSH_TOKEN?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
