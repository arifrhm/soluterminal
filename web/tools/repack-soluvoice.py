#!/usr/bin/env python3
"""Rebuild the canonical vendor source using web's locked TypeScript toolchain."""
import argparse
import gzip
import io
import json
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile

web = Path(__file__).resolve().parents[1]
source = web / 'vendor/soluvoice'
archive = web / 'vendor/soluvoice-0.1.6.tgz'
parser = argparse.ArgumentParser()
parser.add_argument('--check', action='store_true')
args = parser.parse_args()
with tempfile.TemporaryDirectory(prefix='soluvoice-') as temp:
    root = Path(temp) / 'package'
    shutil.copytree(source, root, ignore=shutil.ignore_patterns('dist', 'node_modules'))
    # Resolve React declarations from web without copying or mutating node_modules.
    (root / 'node_modules').symlink_to(web / 'node_modules', target_is_directory=True)
    subprocess.run(['node', str(web / 'node_modules/typescript/bin/tsc'), '-p', str(root / 'tsconfig.json')], check=True)
    # A function's toString() is not stable after an app minifies imported classes.
    # Freeze the processor as a literal while names are still known; Vite can then
    # minify the package safely without changing code executed in the worklet.
    worklet = root / 'dist/pcmWorklet.js'
    compiled = worklet.read_text()
    definitions = compiled.split('export const pcmWorkletSource', 1)[0]
    definitions = definitions.replace('export class ', 'class ')
    script = definitions + "\nregisterProcessor('solu-pcm', PCMProcessor);"
    worklet.write_text(compiled.split('export const pcmWorkletSource', 1)[0] +
                       'export const pcmWorkletSource = ' + json.dumps(script) + ';\n')
    (root / 'node_modules').unlink()
    shutil.copyfile(root / 'src/styles.css', root / 'dist/styles.css')
    output = io.BytesIO()
    with gzip.GzipFile(fileobj=output, mode='wb', filename='', mtime=0) as gz:
        with tarfile.open(fileobj=gz, mode='w', format=tarfile.USTAR_FORMAT) as tar:
            for file in sorted(root.rglob('*')):
                if not file.is_file():
                    continue
                data = file.read_bytes()
                entry = tarfile.TarInfo('package/' + file.relative_to(root).as_posix())
                entry.size = len(data)
                entry.mode = 0o644
                entry.mtime = entry.uid = entry.gid = 0
                entry.uname = entry.gname = ''
                tar.addfile(entry, io.BytesIO(data))
    data = output.getvalue()
    if args.check:
        if not archive.exists() or archive.read_bytes() != data:
            raise SystemExit('soluvoice archive differs: run npm run vendor:soluvoice')
        print('soluvoice archive matches canonical source')
    else:
        archive.write_bytes(data)
        print(f'Wrote {archive.name} ({len(data)} bytes)')
