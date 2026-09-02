package com.cognigate.service;

import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;

import static org.assertj.core.api.Assertions.*;

@DisplayName("EncryptionService — AES-256-GCM Tests")
class EncryptionServiceTest {

    // A valid 32-byte (64-char hex) master key for testing
    private static final String TEST_KEY = "0011223344556677889900aabbccddeeff0011223344556677889900aabbccdd";

    private EncryptionService encryptionService;

    @BeforeEach
    void setUp() {
        encryptionService = new EncryptionService(TEST_KEY);
    }

    @Test
    @DisplayName("encrypt() should produce a non-null, non-empty Base64 string")
    void encrypt_shouldProduceBase64Output() {
        String ciphertext = encryptionService.encrypt("EXAMPLE-PROVIDER-KEY-NOT-REAL");
        assertThat(ciphertext).isNotNull().isNotEmpty();
    }

    @Test
    @DisplayName("decrypt(encrypt(x)) should return the original plaintext")
    void encryptThenDecrypt_shouldReturnOriginal() {
        String original = "EXAMPLE-PROVIDER-KEY-NOT-REAL";
        String ciphertext = encryptionService.encrypt(original);
        String decrypted = encryptionService.decrypt(ciphertext);

        assertThat(decrypted).isEqualTo(original);
    }

    @ParameterizedTest
    @ValueSource(strings = {
        "short",
        "a-medium-length-example-value-1234",
        "an-example-value-long-enough-to-span-several-AES-blocks-abcdefghijklmnop1234567890"
    })
    @DisplayName("encrypt/decrypt should handle various key lengths")
    void encryptDecrypt_variousLengths(String plaintext) {
        String ciphertext = encryptionService.encrypt(plaintext);
        String decrypted = encryptionService.decrypt(ciphertext);
        assertThat(decrypted).isEqualTo(plaintext);
    }

    @Test
    @DisplayName("Two encryptions of the same plaintext should produce different ciphertexts (random IV)")
    void encrypt_shouldUseRandomIV() {
        String plaintext = "same-api-key";
        String cipher1 = encryptionService.encrypt(plaintext);
        String cipher2 = encryptionService.encrypt(plaintext);
        assertThat(cipher1).isNotEqualTo(cipher2);
    }

    @Test
    @DisplayName("decrypt() with tampered ciphertext should throw RuntimeException")
    void decrypt_withTamperedData_shouldThrow() {
        String ciphertext = encryptionService.encrypt("some-key");
        // Tamper: flip a character in the middle of the base64 string
        char[] chars = ciphertext.toCharArray();
        chars[10] = chars[10] == 'A' ? 'B' : 'A';
        String tampered = new String(chars);

        assertThatThrownBy(() -> encryptionService.decrypt(tampered))
            .isInstanceOf(RuntimeException.class);
    }

    @ParameterizedTest
    @ValueSource(strings = {
        "short_key",                                                           // odd length
        "0123456789abcdef0123456789abcdef",                                    // 16 bytes, not 32
        "0011223344556677889900aabbccddeeff0011223344556677889900aabbccddee",  // 33 bytes
        "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",    // 64 chars, not hex
        "0011223344556677889900aabbccddeeff0011223344556677889900aabbccdX"     // one non-hex char
    })
    @DisplayName("Constructor should reject any master key that is not 64 hex characters")
    void constructor_withInvalidKey_shouldThrow(String badKey) {
        assertThatThrownBy(() -> new EncryptionService(badKey))
            .isInstanceOf(IllegalArgumentException.class)
            .hasMessageContaining("256 bits");
    }

    @ParameterizedTest
    @ValueSource(strings = {"", "   "})
    @DisplayName("Constructor should reject an unset master key rather than fall back to a default")
    void constructor_withUnsetKey_shouldThrow(String unset) {
        assertThatThrownBy(() -> new EncryptionService(unset))
            .isInstanceOf(IllegalArgumentException.class)
            .hasMessageContaining("not set");
    }

    @Test
    @DisplayName("Constructor should reject a null master key")
    void constructor_withNullKey_shouldThrow() {
        assertThatThrownBy(() -> new EncryptionService(null))
            .isInstanceOf(IllegalArgumentException.class)
            .hasMessageContaining("not set");
    }
}
