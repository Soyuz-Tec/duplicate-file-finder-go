#requires -Version 5.1

[CmdletBinding()]
param(
    [switch]$Check
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$sourcePath = Join-Path $repoRoot "cmd\twintidy\winres\icon.png"
$assetDirectory = Join-Path $repoRoot "packaging\msix\Assets"
$assetSizes = [ordered]@{
    "Square44x44Logo.png" = 44
    "Square150x150Logo.png" = 150
    "StoreLogo.png" = 50
}

if (-not [System.IO.File]::Exists($sourcePath)) {
    throw "TwinTidy icon source is missing: $sourcePath"
}

# GDI+ scaling and PNG compression are not bit-identical across Windows
# architectures or OS builds, which breaks `-Check` as a cross-runner release
# gate. The assets are therefore derived with integer-only box filtering over
# premultiplied alpha and serialized as fixed stored-block PNG bytes: every
# step is exact integer arithmetic or losslessly specified, so each supported
# host reproduces identical bytes.
$assetGeneratorSource = @'
using System;
using System.IO;
using System.IO.Compression;

namespace TwinTidy.Packaging
{
    public static class DeterministicMsixAssets
    {
        private static readonly byte[] PngSignature = new byte[] { 137, 80, 78, 71, 13, 10, 26, 10 };
        private static readonly uint[] Crc32Table = BuildCrc32Table();

        public static byte[] Render(byte[] sourcePng, int targetSize)
        {
            if (sourcePng == null)
            {
                throw new ArgumentNullException("sourcePng");
            }
            if (targetSize < 1 || targetSize > 4096)
            {
                throw new ArgumentOutOfRangeException("targetSize");
            }
            int sourceWidth;
            int sourceHeight;
            byte[] sourcePixels = DecodeRgba8Png(sourcePng, out sourceWidth, out sourceHeight);
            byte[] targetPixels = ScaleBoxRgba(sourcePixels, sourceWidth, sourceHeight, targetSize, targetSize);
            return EncodeRgba8Png(targetPixels, targetSize, targetSize);
        }

        private static byte[] DecodeRgba8Png(byte[] data, out int width, out int height)
        {
            if (data.Length < PngSignature.Length + 12)
            {
                throw new InvalidDataException("PNG source is truncated.");
            }
            for (int i = 0; i < PngSignature.Length; i++)
            {
                if (data[i] != PngSignature[i])
                {
                    throw new InvalidDataException("PNG source signature is invalid.");
                }
            }

            width = 0;
            height = 0;
            bool headerSeen = false;
            bool endSeen = false;
            MemoryStream zlibStream = new MemoryStream();
            int offset = PngSignature.Length;
            while (offset < data.Length)
            {
                if (endSeen)
                {
                    throw new InvalidDataException("PNG source has bytes after IEND.");
                }
                if (data.Length - offset < 12)
                {
                    throw new InvalidDataException("PNG source chunk is truncated.");
                }
                uint declaredLength = ReadUInt32BigEndian(data, offset);
                if (declaredLength > (uint)int.MaxValue - 12u || (long)data.Length - offset - 12 < declaredLength)
                {
                    throw new InvalidDataException("PNG source chunk length is invalid.");
                }
                int length = (int)declaredLength;
                string chunkType = ReadChunkType(data, offset + 4);
                uint expectedCrc = ReadUInt32BigEndian(data, offset + 8 + length);
                uint actualCrc = Crc32(data, offset + 4, length + 4);
                if (expectedCrc != actualCrc)
                {
                    throw new InvalidDataException("PNG source chunk '" + chunkType + "' fails its CRC-32 check.");
                }
                if (!headerSeen && chunkType != "IHDR")
                {
                    throw new InvalidDataException("PNG source does not start with IHDR.");
                }

                if (chunkType == "IHDR")
                {
                    if (headerSeen || length != 13)
                    {
                        throw new InvalidDataException("PNG source IHDR is invalid.");
                    }
                    width = (int)ReadUInt32BigEndian(data, offset + 8);
                    height = (int)ReadUInt32BigEndian(data, offset + 12);
                    int bitDepth = data[offset + 16];
                    int colorType = data[offset + 17];
                    int compression = data[offset + 18];
                    int filterMethod = data[offset + 19];
                    int interlace = data[offset + 20];
                    if (width < 1 || height < 1 || width > 16384 || height > 16384)
                    {
                        throw new InvalidDataException("PNG source dimensions are unsupported.");
                    }
                    if (bitDepth != 8 || colorType != 6 || compression != 0 || filterMethod != 0 || interlace != 0)
                    {
                        throw new InvalidDataException("TwinTidy icon must be an 8-bit RGBA non-interlaced PNG.");
                    }
                    headerSeen = true;
                }
                else if (chunkType == "IDAT")
                {
                    zlibStream.Write(data, offset + 8, length);
                }
                else if (chunkType == "IEND")
                {
                    if (length != 0)
                    {
                        throw new InvalidDataException("PNG source IEND is invalid.");
                    }
                    endSeen = true;
                }
                offset += 12 + length;
            }
            if (!endSeen)
            {
                throw new InvalidDataException("PNG source is missing IEND.");
            }

            byte[] zlib = zlibStream.ToArray();
            if (zlib.Length < 6)
            {
                throw new InvalidDataException("PNG source pixel stream is truncated.");
            }
            if ((zlib[0] & 0x0F) != 8)
            {
                throw new InvalidDataException("PNG source pixel stream is not deflate-compressed.");
            }
            if ((zlib[1] & 0x20) != 0)
            {
                throw new InvalidDataException("PNG source uses an unsupported preset dictionary.");
            }
            if (((zlib[0] * 256) + zlib[1]) % 31 != 0)
            {
                throw new InvalidDataException("PNG source zlib header check failed.");
            }

            int stride = width * 4;
            byte[] raw = new byte[(long)height * (stride + 1)];
            using (MemoryStream deflated = new MemoryStream(zlib, 2, zlib.Length - 6, false))
            using (DeflateStream inflater = new DeflateStream(deflated, CompressionMode.Decompress))
            {
                int total = 0;
                while (total < raw.Length)
                {
                    int read = inflater.Read(raw, total, raw.Length - total);
                    if (read <= 0)
                    {
                        throw new InvalidDataException("PNG source pixel stream is shorter than its header declares.");
                    }
                    total += read;
                }
                byte[] excess = new byte[1];
                if (inflater.Read(excess, 0, 1) != 0)
                {
                    throw new InvalidDataException("PNG source pixel stream is longer than its header declares.");
                }
            }
            if (Adler32(raw) != ReadUInt32BigEndian(zlib, zlib.Length - 4))
            {
                throw new InvalidDataException("PNG source pixel stream fails its Adler-32 check.");
            }

            return UnfilterRgba(raw, width, height);
        }

        private static byte[] UnfilterRgba(byte[] raw, int width, int height)
        {
            int stride = width * 4;
            byte[] pixels = new byte[height * stride];
            int rawIndex = 0;
            for (int y = 0; y < height; y++)
            {
                int filter = raw[rawIndex];
                rawIndex++;
                int rowStart = y * stride;
                int previousRowStart = rowStart - stride;
                for (int x = 0; x < stride; x++)
                {
                    int value = raw[rawIndex + x];
                    int left = x >= 4 ? pixels[rowStart + x - 4] : 0;
                    int above = y > 0 ? pixels[previousRowStart + x] : 0;
                    int aboveLeft = (y > 0 && x >= 4) ? pixels[previousRowStart + x - 4] : 0;
                    int reconstructed;
                    switch (filter)
                    {
                        case 0:
                            reconstructed = value;
                            break;
                        case 1:
                            reconstructed = value + left;
                            break;
                        case 2:
                            reconstructed = value + above;
                            break;
                        case 3:
                            reconstructed = value + ((left + above) / 2);
                            break;
                        case 4:
                            reconstructed = value + PaethPredictor(left, above, aboveLeft);
                            break;
                        default:
                            throw new InvalidDataException("PNG source uses an unsupported scanline filter.");
                    }
                    pixels[rowStart + x] = (byte)(reconstructed & 0xFF);
                }
                rawIndex += stride;
            }
            return pixels;
        }

        private static int PaethPredictor(int left, int above, int aboveLeft)
        {
            int estimate = left + above - aboveLeft;
            int distanceLeft = Math.Abs(estimate - left);
            int distanceAbove = Math.Abs(estimate - above);
            int distanceAboveLeft = Math.Abs(estimate - aboveLeft);
            if (distanceLeft <= distanceAbove && distanceLeft <= distanceAboveLeft)
            {
                return left;
            }
            if (distanceAbove <= distanceAboveLeft)
            {
                return above;
            }
            return aboveLeft;
        }

        private static void BuildAxisWeights(int sourceSize, int targetSize, out int[] starts, out int[][] weights)
        {
            starts = new int[targetSize];
            weights = new int[targetSize][];
            for (int i = 0; i < targetSize; i++)
            {
                long intervalStart = (long)i * sourceSize;
                long intervalEnd = (long)(i + 1) * sourceSize;
                int firstPixel = (int)(intervalStart / targetSize);
                int lastPixel = (int)((intervalEnd - 1) / targetSize);
                starts[i] = firstPixel;
                int[] pixelWeights = new int[lastPixel - firstPixel + 1];
                for (int j = 0; j < pixelWeights.Length; j++)
                {
                    long pixelStart = (long)(firstPixel + j) * targetSize;
                    long pixelEnd = pixelStart + targetSize;
                    pixelWeights[j] = (int)(Math.Min(intervalEnd, pixelEnd) - Math.Max(intervalStart, pixelStart));
                }
                weights[i] = pixelWeights;
            }
        }

        private static byte[] ScaleBoxRgba(byte[] source, int sourceWidth, int sourceHeight, int targetWidth, int targetHeight)
        {
            int[] xStarts;
            int[][] xWeights;
            int[] yStarts;
            int[][] yWeights;
            BuildAxisWeights(sourceWidth, targetWidth, out xStarts, out xWeights);
            BuildAxisWeights(sourceHeight, targetHeight, out yStarts, out yWeights);

            long totalWeight = (long)sourceWidth * sourceHeight;
            byte[] target = new byte[targetWidth * targetHeight * 4];
            for (int oy = 0; oy < targetHeight; oy++)
            {
                int[] rowWeights = yWeights[oy];
                int yStart = yStarts[oy];
                for (int ox = 0; ox < targetWidth; ox++)
                {
                    int[] columnWeights = xWeights[ox];
                    int xStart = xStarts[ox];
                    long alphaSum = 0;
                    long redSum = 0;
                    long greenSum = 0;
                    long blueSum = 0;
                    for (int j = 0; j < rowWeights.Length; j++)
                    {
                        long rowWeight = rowWeights[j];
                        int rowOffset = ((yStart + j) * sourceWidth + xStart) * 4;
                        for (int k = 0; k < columnWeights.Length; k++)
                        {
                            int pixel = rowOffset + k * 4;
                            int alpha = source[pixel + 3];
                            if (alpha != 0)
                            {
                                long alphaWeight = rowWeight * columnWeights[k] * alpha;
                                redSum += alphaWeight * source[pixel];
                                greenSum += alphaWeight * source[pixel + 1];
                                blueSum += alphaWeight * source[pixel + 2];
                                alphaSum += alphaWeight;
                            }
                        }
                    }
                    int targetIndex = (oy * targetWidth + ox) * 4;
                    if (alphaSum > 0)
                    {
                        target[targetIndex] = (byte)Math.Min(255, (redSum + alphaSum / 2) / alphaSum);
                        target[targetIndex + 1] = (byte)Math.Min(255, (greenSum + alphaSum / 2) / alphaSum);
                        target[targetIndex + 2] = (byte)Math.Min(255, (blueSum + alphaSum / 2) / alphaSum);
                        target[targetIndex + 3] = (byte)Math.Min(255, (alphaSum + totalWeight / 2) / totalWeight);
                    }
                }
            }
            return target;
        }

        private static byte[] EncodeRgba8Png(byte[] pixels, int width, int height)
        {
            int stride = width * 4;
            byte[] raw = new byte[height * (stride + 1)];
            int rawIndex = 0;
            for (int y = 0; y < height; y++)
            {
                raw[rawIndex] = 0;
                rawIndex++;
                Buffer.BlockCopy(pixels, y * stride, raw, rawIndex, stride);
                rawIndex += stride;
            }

            MemoryStream output = new MemoryStream();
            output.Write(PngSignature, 0, PngSignature.Length);
            byte[] header = new byte[13];
            WriteUInt32BigEndian(header, 0, (uint)width);
            WriteUInt32BigEndian(header, 4, (uint)height);
            header[8] = 8;
            header[9] = 6;
            header[10] = 0;
            header[11] = 0;
            header[12] = 0;
            WriteChunk(output, "IHDR", header);
            WriteChunk(output, "IDAT", ZlibStored(raw));
            WriteChunk(output, "IEND", new byte[0]);
            return output.ToArray();
        }

        private static byte[] ZlibStored(byte[] raw)
        {
            MemoryStream stream = new MemoryStream();
            stream.WriteByte(0x78);
            stream.WriteByte(0x01);
            int offset = 0;
            do
            {
                int blockLength = Math.Min(65535, raw.Length - offset);
                bool final = offset + blockLength >= raw.Length;
                stream.WriteByte(final ? (byte)1 : (byte)0);
                stream.WriteByte((byte)(blockLength & 0xFF));
                stream.WriteByte((byte)((blockLength >> 8) & 0xFF));
                stream.WriteByte((byte)(~blockLength & 0xFF));
                stream.WriteByte((byte)((~blockLength >> 8) & 0xFF));
                stream.Write(raw, offset, blockLength);
                offset += blockLength;
            } while (offset < raw.Length);
            uint adler = Adler32(raw);
            stream.WriteByte((byte)(adler >> 24));
            stream.WriteByte((byte)(adler >> 16));
            stream.WriteByte((byte)(adler >> 8));
            stream.WriteByte((byte)adler);
            return stream.ToArray();
        }

        private static void WriteChunk(MemoryStream output, string chunkType, byte[] body)
        {
            byte[] lengthBytes = new byte[4];
            WriteUInt32BigEndian(lengthBytes, 0, (uint)body.Length);
            output.Write(lengthBytes, 0, 4);
            byte[] typeAndBody = new byte[4 + body.Length];
            for (int i = 0; i < 4; i++)
            {
                typeAndBody[i] = (byte)chunkType[i];
            }
            Buffer.BlockCopy(body, 0, typeAndBody, 4, body.Length);
            output.Write(typeAndBody, 0, typeAndBody.Length);
            byte[] crcBytes = new byte[4];
            WriteUInt32BigEndian(crcBytes, 0, Crc32(typeAndBody, 0, typeAndBody.Length));
            output.Write(crcBytes, 0, 4);
        }

        private static string ReadChunkType(byte[] data, int offset)
        {
            char[] chunkType = new char[4];
            for (int i = 0; i < 4; i++)
            {
                byte value = data[offset + i];
                bool upper = value >= 65 && value <= 90;
                bool lower = value >= 97 && value <= 122;
                if (!upper && !lower)
                {
                    throw new InvalidDataException("PNG source chunk type is invalid.");
                }
                chunkType[i] = (char)value;
            }
            return new string(chunkType);
        }

        private static uint ReadUInt32BigEndian(byte[] buffer, int offset)
        {
            return ((uint)buffer[offset] << 24) |
                ((uint)buffer[offset + 1] << 16) |
                ((uint)buffer[offset + 2] << 8) |
                buffer[offset + 3];
        }

        private static void WriteUInt32BigEndian(byte[] buffer, int offset, uint value)
        {
            buffer[offset] = (byte)(value >> 24);
            buffer[offset + 1] = (byte)(value >> 16);
            buffer[offset + 2] = (byte)(value >> 8);
            buffer[offset + 3] = (byte)value;
        }

        private static uint[] BuildCrc32Table()
        {
            uint[] table = new uint[256];
            for (uint n = 0; n < 256; n++)
            {
                uint c = n;
                for (int k = 0; k < 8; k++)
                {
                    c = (c & 1u) != 0 ? 0xEDB88320u ^ (c >> 1) : c >> 1;
                }
                table[n] = c;
            }
            return table;
        }

        private static uint Crc32(byte[] data, int offset, int count)
        {
            uint c = 0xFFFFFFFFu;
            for (int i = 0; i < count; i++)
            {
                c = Crc32Table[(int)((c ^ data[offset + i]) & 0xFFu)] ^ (c >> 8);
            }
            return c ^ 0xFFFFFFFFu;
        }

        private static uint Adler32(byte[] data)
        {
            uint a = 1;
            uint b = 0;
            for (int i = 0; i < data.Length; i++)
            {
                a = (a + data[i]) % 65521u;
                b = (b + a) % 65521u;
            }
            return (b << 16) | a;
        }
    }
}
'@

if (-not ("TwinTidy.Packaging.DeterministicMsixAssets" -as [type])) {
    if ($PSVersionTable.PSEdition -ceq "Core") {
        Add-Type -TypeDefinition $assetGeneratorSource -ReferencedAssemblies @(
            "System.Runtime",
            "System.IO.Compression"
        )
    } else {
        Add-Type -TypeDefinition $assetGeneratorSource
    }
}

$sourceBytes = [System.IO.File]::ReadAllBytes($sourcePath)
$hasher = [System.Security.Cryptography.SHA256]::Create()
try {
    foreach ($asset in $assetSizes.GetEnumerator()) {
        $generated = [TwinTidy.Packaging.DeterministicMsixAssets]::Render($sourceBytes, $asset.Value)
        $expected = [System.BitConverter]::ToString($hasher.ComputeHash($generated)).Replace("-", "")
        $tracked = Join-Path $assetDirectory $asset.Key
        if ($Check) {
            if (-not [System.IO.File]::Exists($tracked)) {
                throw "Tracked MSIX asset is missing: $tracked"
            }
            $actual = (Get-FileHash -LiteralPath $tracked -Algorithm SHA256).Hash
            if ($actual -cne $expected) {
                throw "MSIX asset '$($asset.Key)' is not the deterministic derivative of the TwinTidy icon."
            }
            [pscustomobject]@{ Asset = $asset.Key; Size = $asset.Value; Deterministic = $true; SHA256 = $actual }
        } else {
            [System.IO.Directory]::CreateDirectory($assetDirectory) | Out-Null
            [System.IO.File]::WriteAllBytes($tracked, $generated)
        }
    }
} finally {
    $hasher.Dispose()
}
