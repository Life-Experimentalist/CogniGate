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
        String ciphertext = encryptionService.encrypt("sk-ant-prod-key-12345");
        assertThat(ciphertext).isNotNull().isNotEmpty();
    }

    @Test
    @DisplayName("decrypt(encrypt(x)) should return the original plaintext")
    void encryptThenDecrypt_shouldReturnOriginal() {
        String original = "sk-openai-very-secret-api-key";
        String ciphertext = encryptionService.encrypt(original);
        String decrypted = encryptionService.decrypt(ciphertext);

        assertThat(decrypted).isEqualTo(original);
    }

    @ParameterizedTest
    @ValueSource(strings = {
        "short",
        "a-medium-length-api-key-1234",
        "sk-ant-api03-very-long-enterprise-key-with-lots-of-characters-abcdefghijklmnop1234567890"
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

    @Test
    @DisplayName("Constructor should throw when master key is not 32 bytes")
    void constructor_withInvalidKeyLength_shouldThrow() {
        assertThatThrownBy(() -> new EncryptionService("short_key"))
            .isInstanceOf(IllegalArgumentException.class)
            .hasMessageContaining("256 bits");
    }
}
