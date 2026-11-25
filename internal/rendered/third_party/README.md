# Third-party scripts

Bundled into the app: Pele for diagram blocks, which render in a sandbox
with no network, and MapLibre GL JS for map blocks on the Mac and
Windows, whose pages reach only their tile server (OpenFreeMap). Each
comes from npm, checked against the registry's integrity hash, with its
license beside it: Pele's is MIT, MapLibre's BSD-3-Clause. The files are
as the tarball has them; the app leaves out their source map comments as
it builds a page of them.

| Package | Version | Files | Integrity of the npm tarball |
| --- | --- | --- | --- |
| [pele](https://github.com/obsidianmd/pele) | 1.13.0 | `dist/pele.js` | `sha512-Ie8UKY9x00MsuR3jO6AzUwzK2Z6MIV3Tf4EDdITiuk8Uaz8K7g0R082WqsqNaSU8DdgkrRvZThzhit83mi/uiw==` |
| [maplibre-gl](https://github.com/maplibre/maplibre-gl-js) | 6.17.0 | `dist/maplibre-gl.mjs`, `dist/maplibre-gl-worker.mjs`, `dist/maplibre-gl.css` | `sha512-2chIZwpLGenoUQOcnm9Pk4SLbA8ppNFq8jlpXiq9vcYm3Y1wJ3fc82FL0GLwBaPYJCLzPv/mC46KEupVov75rQ==` |
