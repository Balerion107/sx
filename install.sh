#!/bin/bash
set -e

# Install script for sx CLI
# Downloads the latest release binary from GitHub

# Detect OS and architecture
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

# Map architecture names to match GoReleaser output
case "$ARCH" in
    x86_64)
        ARCH="x86_64"
        ;;
    aarch64|arm64)
        ARCH="arm64"
        ;;
    *)
        echo "Unsupported architecture: $ARCH"
        exit 1
        ;;
esac

# Map OS names to match GoReleaser output
case "$OS" in
    linux)
        OS="Linux"
        EXT="tar.gz"
        ;;
    darwin)
        OS="Darwin"
        EXT="tar.gz"
        ;;
    mingw*|msys*|cygwin*)
        OS="Windows"
        EXT="zip"
        ;;
    *)
        echo "Unsupported OS: $OS"
        exit 1
        ;;
esac

# Get latest release version
echo "Fetching latest release..."
VERSION=$(curl -s https://api.github.com/repos/sleuth-io/sx/releases/latest | grep '"tag_name"' | cut -d'"' -f4)

if [ -z "$VERSION" ]; then
    echo "Error: Could not fetch latest version"
    exit 1
fi

echo "Installing sx ${VERSION} for ${OS}_${ARCH}..."

# Build download URL
BINARY_NAME="sx_${OS}_${ARCH}.${EXT}"
URL="https://github.com/sleuth-io/sx/releases/download/${VERSION}/${BINARY_NAME}"

# Determine install location
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
mkdir -p "$INSTALL_DIR"

# Download and extract
TEMP_DIR=$(mktemp -d)
cd "$TEMP_DIR"

echo "Downloading from ${URL}..."
if ! curl -fsSL "$URL" -o "$BINARY_NAME"; then
    echo "Error: Failed to download binary"
    rm -rf "$TEMP_DIR"
    exit 1
fi

# Verify the download against the release's published checksums. This script is
# meant to be piped straight into a shell, so an unverified archive gets
# extracted and executed on the user's machine — a corrupted mirror, a truncated
# transfer, or a tampered artifact would otherwise install silently.
CHECKSUM_URL="https://github.com/sleuth-io/sx/releases/download/${VERSION}/checksums.txt"
echo "Verifying checksum..."
if ! curl -fsSL "$CHECKSUM_URL" -o checksums.txt; then
    echo "Error: failed to download checksums.txt from ${CHECKSUM_URL}"
    echo "Refusing to install an unverified binary."
    rm -rf "$TEMP_DIR"
    exit 1
fi

# Use whichever hashing tool the platform ships: sha256sum on most Linux
# images, shasum on macOS.
if command -v sha256sum > /dev/null 2>&1; then
    SHA_CMD="sha256sum"
elif command -v shasum > /dev/null 2>&1; then
    SHA_CMD="shasum -a 256"
else
    echo "Error: neither sha256sum nor shasum is available; cannot verify the download."
    echo "Install one of them, or download and verify manually against:"
    echo "  ${CHECKSUM_URL}"
    rm -rf "$TEMP_DIR"
    exit 1
fi

EXPECTED=$(grep " ${BINARY_NAME}\$" checksums.txt | awk '{print $1}')
if [ -z "$EXPECTED" ]; then
    echo "Error: ${BINARY_NAME} is not listed in checksums.txt"
    rm -rf "$TEMP_DIR"
    exit 1
fi

ACTUAL=$($SHA_CMD "$BINARY_NAME" | awk '{print $1}')
if [ "$EXPECTED" != "$ACTUAL" ]; then
    echo "Error: checksum mismatch for ${BINARY_NAME}"
    echo "  expected: ${EXPECTED}"
    echo "  actual:   ${ACTUAL}"
    echo "Refusing to install. Please report this at https://github.com/sleuth-io/sx/issues"
    rm -rf "$TEMP_DIR"
    exit 1
fi
echo "✓ Checksum verified"

# Extract based on file type
if [ "$EXT" = "tar.gz" ]; then
    tar -xzf "$BINARY_NAME"
elif [ "$EXT" = "zip" ]; then
    unzip -q "$BINARY_NAME"
fi

# Install binary
chmod +x sx
mv sx "$INSTALL_DIR/"

# Cleanup
cd - > /dev/null
rm -rf "$TEMP_DIR"

echo "✓ sx installed to $INSTALL_DIR/sx"

# Check if install dir is in PATH
if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
    echo ""
    echo "⚠ Warning: $INSTALL_DIR is not in your PATH"
    echo "Add this to your ~/.bashrc or ~/.zshrc:"
    echo "  export PATH=\"\$PATH:$INSTALL_DIR\""
fi

# Verify installation
if command -v sx &> /dev/null; then
    echo ""
    sx --version
else
    echo ""
    echo "Run 'source ~/.bashrc' (or restart your shell) and then try: sx --version"
fi
