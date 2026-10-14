"""
Generic S3-backed filesystem cache (content-addressed tar + zstd bundles).

Intentionally free of any project specifics so it can later move into praktika
as-is. A bundle is a set of paths archived together and keyed by a caller-chosen
string; `restore` extracts it, `save` creates it **atomically write-once**. Large
bundles use multipart transfers.

Write-once is enforced server-side with an S3 conditional write (If-None-Match:
"*") on the finalizing call — PutObject for small bundles, CompleteMultipartUpload
for large ones. The first writer wins; a loser's conditional fails with
PreconditionFailed (treated as success — the entry is already populated), so
concurrent first-run builders can't clobber or interleave a half-written object.
This also makes entries immutable: an existing key is never overwritten.

Example (restore-or-build-and-save):

    cache = S3PathCache(bucket="my-bucket", prefix="ci_cache", region="eu-north-1")
    key = S3PathCache.key_from(["go1.27.0", "arm64", helm_ver])
    if not cache.restore(key, namespace="go-env"):
        ...install the toolchain into the paths below...
        cache.save(key, ["/usr/local/go", "/root/go"], namespace="go-env")
"""
import hashlib
import os
import subprocess
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from typing import List

import boto3
from boto3.s3.transfer import TransferConfig
from botocore.exceptions import ClientError

_MULTIPART_THRESHOLD = 16 * 1024 * 1024
_MULTIPART_CHUNKSIZE = 32 * 1024 * 1024
_MAX_CONCURRENCY = 8

# Multipart, parallel download. (Upload is driven manually in _upload_write_once
# so the finalizing call can carry the If-None-Match conditional, which the
# high-level upload_file/ExtraArgs path does not support.)
_TRANSFER = TransferConfig(
    multipart_threshold=_MULTIPART_THRESHOLD,
    multipart_chunksize=_MULTIPART_CHUNKSIZE,
    max_concurrency=_MAX_CONCURRENCY,
    use_threads=True,
)


def _is_precondition_failed(e: ClientError) -> bool:
    code = e.response.get("Error", {}).get("Code", "")
    status = e.response.get("ResponseMetadata", {}).get("HTTPStatusCode")
    return code in ("PreconditionFailed", "ConditionalRequestConflict") or status == 412


class S3PathCache:
    def __init__(self, bucket: str, prefix: str, region: str, tmp_dir: str = "./ci/tmp"):
        self.bucket = bucket
        self.prefix = prefix.strip("/")
        self.tmp_dir = tmp_dir
        self._client = boto3.client("s3", region_name=region)

    @staticmethod
    def key_from(parts: List[str]) -> str:
        """Short, stable cache key from the given parts."""
        joined = "\n".join(str(p) for p in parts)
        return hashlib.sha256(joined.encode()).hexdigest()[:16]

    def _s3_key(self, key: str, namespace: str) -> str:
        return f"{self.prefix}/{namespace}/{key}.tar.zst"

    def _uri(self, s3_key: str) -> str:
        return f"s3://{self.bucket}/{s3_key}"

    def exists(self, key: str, namespace: str) -> bool:
        s3_key = self._s3_key(key, namespace)
        try:
            self._client.head_object(Bucket=self.bucket, Key=s3_key)
            return True
        except ClientError as e:
            if e.response.get("Error", {}).get("Code") in ("404", "NoSuchKey", "NotFound"):
                return False
            raise

    def restore(self, key: str, namespace: str, base: str = "/") -> bool:
        """Download + extract the bundle under ``base``. True on hit, False on miss
        or a failed/corrupt extraction."""
        s3_key = self._s3_key(key, namespace)
        if not self.exists(key, namespace):
            print(f"S3 cache miss: {self._uri(s3_key)}")
            return False
        Path(self.tmp_dir).mkdir(parents=True, exist_ok=True)
        local = os.path.join(self.tmp_dir, f"cache_{key}.tar.zst")
        print(f"S3 cache hit, downloading: {self._uri(s3_key)}")
        self._client.download_file(self.bucket, s3_key, local, Config=_TRANSFER)
        try:
            subprocess.run(
                f"zstd -d {local} --stdout | tar -xf - -C {base}",
                shell=True, check=True,
            )
        except subprocess.CalledProcessError as e:
            print(f"WARNING: cache extraction failed: {e}")
            return False
        finally:
            _rm(local)
        return True

    def save(self, key: str, paths: List[str], namespace: str, base: str = "/") -> bool:
        """Archive ``paths`` (relative to ``base``) and upload atomically
        write-once. Existing entries are never overwritten; on a lost first-run
        race the conditional upload fails cleanly (another writer populated it)."""
        s3_key = self._s3_key(key, namespace)
        # Fast path: skip building the tarball when already cached. The upload is
        # still conditional, so this is an optimization, not the correctness gate.
        if self.exists(key, namespace):
            print(f"S3 cache already present, skip save: {self._uri(s3_key)}")
            return True

        rel = []
        for p in paths:
            if Path(p).exists():
                rel.append(os.path.relpath(p, base))
            else:
                print(f"WARNING: skipping missing path [{p}]")
        if not rel:
            print("WARNING: nothing to cache")
            return False

        Path(self.tmp_dir).mkdir(parents=True, exist_ok=True)
        local = os.path.join(self.tmp_dir, f"cache_{key}.tar.zst")
        subprocess.run(
            f"tar -C {base} -cf - {' '.join(rel)} | zstd -c -T0 > {local}",
            shell=True, check=True,
        )
        try:
            won = self._upload_write_once(local, s3_key)
            print(
                f"S3 cache {'saved' if won else 'already populated by another run'}: "
                f"{self._uri(s3_key)}"
            )
        finally:
            _rm(local)
        return True

    def download(self, key: str, namespace: str, dest: str) -> bool:
        """Download the raw bundle object to ``dest`` (no extraction). Returns True
        on a hit, False on a miss. Use when another layer (e.g. a praktika
        artifact) distributes the tarball itself."""
        s3_key = self._s3_key(key, namespace)
        if not self.exists(key, namespace):
            print(f"S3 cache miss: {self._uri(s3_key)}")
            return False
        Path(dest).parent.mkdir(parents=True, exist_ok=True)
        print(f"S3 cache hit, downloading: {self._uri(s3_key)}")
        self._client.download_file(self.bucket, s3_key, dest, Config=_TRANSFER)
        return True

    def upload(self, key: str, namespace: str, src: str) -> bool:
        """Upload an existing bundle file ``src`` write-once (no tarring). No-op if
        the entry already exists."""
        s3_key = self._s3_key(key, namespace)
        if self.exists(key, namespace):
            print(f"S3 cache already present, skip upload: {self._uri(s3_key)}")
            return True
        won = self._upload_write_once(src, s3_key)
        print(
            f"S3 cache {'saved' if won else 'already populated by another run'}: "
            f"{self._uri(s3_key)}"
        )
        return True

    def _upload_write_once(self, local: str, s3_key: str) -> bool:
        """Upload with a server-side If-None-Match:* conditional on the finalizing
        call. Returns True if this writer created the object, False if it lost the
        race (object already exists). Raises on any other error."""
        size = os.path.getsize(local)
        try:
            if size < _MULTIPART_THRESHOLD:
                with open(local, "rb") as f:
                    self._client.put_object(
                        Bucket=self.bucket, Key=s3_key, Body=f, IfNoneMatch="*"
                    )
            else:
                self._multipart_upload_write_once(local, s3_key, size)
            return True
        except ClientError as e:
            if _is_precondition_failed(e):
                return False
            raise

    def _multipart_upload_write_once(self, local: str, s3_key: str, size: int) -> None:
        """Manual multipart upload whose CompleteMultipartUpload carries
        If-None-Match:* (the high-level transfer API cannot). Aborts the upload on
        any failure so no orphaned parts are left behind."""
        mpu = self._client.create_multipart_upload(Bucket=self.bucket, Key=s3_key)
        upload_id = mpu["UploadId"]
        try:
            n_parts = (size + _MULTIPART_CHUNKSIZE - 1) // _MULTIPART_CHUNKSIZE

            def _upload_part(idx):
                with open(local, "rb") as f:
                    f.seek(idx * _MULTIPART_CHUNKSIZE)
                    data = f.read(_MULTIPART_CHUNKSIZE)
                resp = self._client.upload_part(
                    Bucket=self.bucket, Key=s3_key, UploadId=upload_id,
                    PartNumber=idx + 1, Body=data,
                )
                return {"PartNumber": idx + 1, "ETag": resp["ETag"]}

            with ThreadPoolExecutor(max_workers=_MAX_CONCURRENCY) as ex:
                parts = list(ex.map(_upload_part, range(n_parts)))
            parts.sort(key=lambda p: p["PartNumber"])

            self._client.complete_multipart_upload(
                Bucket=self.bucket, Key=s3_key, UploadId=upload_id,
                MultipartUpload={"Parts": parts}, IfNoneMatch="*",
            )
        except Exception:
            try:
                self._client.abort_multipart_upload(
                    Bucket=self.bucket, Key=s3_key, UploadId=upload_id
                )
            except ClientError:
                pass
            raise


def _rm(path: str) -> None:
    try:
        os.remove(path)
    except OSError:
        pass
