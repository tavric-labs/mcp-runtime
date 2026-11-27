import struct
import json
from pathlib import Path
import unittest
from unittest.mock import patch

from image_architecture import validate_elf, validate_image


def header(machine: int, byte_order: int = 1) -> bytes:
    data = bytearray(64)
    data[:6] = b"\x7fELF\x02" + bytes([byte_order])
    struct.pack_into("<H" if byte_order == 1 else ">H", data, 18, machine)
    return bytes(data)


class ImageArchitectureTests(unittest.TestCase):
    def test_explicit_registry_binary_with_shell_entrypoint(self):
        metadata = [{"Os": "linux", "Architecture": "arm64", "Config": {"Entrypoint": ["/entrypoint.sh"]}}]
        copied = []

        def run(args, **kwargs):
            if args[:2] == ["docker", "cp"]:
                copied.append(args[2])
                Path(args[3]).write_bytes(header(183))

        with patch("image_architecture.subprocess.check_output", side_effect=[json.dumps(metadata).encode(), "fixture-container\n"]), patch("image_architecture.subprocess.run", side_effect=run):
            validate_image("registry-fixture", "arm64", binary_path="/bin/registry")
        self.assertEqual(copied, ["fixture-container:/bin/registry"])

    def test_native_binaries(self):
        validate_elf(header(183), "arm64")
        validate_elf(header(62), "amd64")
        validate_elf(header(183, 2), "arm64")

    def test_rejects_amd64_executable_in_arm64_image(self):
        with self.assertRaisesRegex(ValueError, "does not match arm64"):
            validate_elf(header(62), "arm64")

    def test_rejects_arm64_executable_in_amd64_image(self):
        with self.assertRaisesRegex(ValueError, "does not match amd64"):
            validate_elf(header(183), "amd64")

    def test_rejects_invalid_executable(self):
        for data in [b"", b"not an ELF binary", b"\x7fELF\x01" + bytes(60), b"\x7fELF\x02\x00" + bytes(58)]:
            with self.subTest(data=data[:6]), self.assertRaises(ValueError):
                validate_elf(data, "arm64")


if __name__ == "__main__":
    unittest.main()
