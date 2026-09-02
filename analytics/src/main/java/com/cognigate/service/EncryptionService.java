package com.cognigate.service;

import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Service;

import javax.crypto.Cipher;
import javax.crypto.spec.GCMParameterSpec;
import javax.crypto.spec.SecretKeySpec;
import java.nio.charset.StandardCharsets;
import java.security.SecureRandom;
import java.util.Base64;

@Service
public class EncryptionService {

    private static final String ALGORITHM = "AES/GCM/NoPadding";
    private static final int TAG_LENGTH_BIT = 128;
    private static final int IV_LENGTH_BYTE = 12;

    private final byte[] masterKey;

    private static final int KEY_LENGTH_BYTE = 32;
    private static final int KEY_LENGTH_HEX = KEY_LENGTH_BYTE * 2;

    /**
     * There is deliberately no default master key: a built-in fallback would
     * be public knowledge and every deployment that forgot to set the
     * variable would encrypt provider keys under it. Absent or malformed
     * configuration fails fast at startup instead.
     */
    public EncryptionService(@Value("${ENCRYPTION_MASTER_KEY:}") String masterKeyHex) {
        this.masterKey = decodeMasterKey(masterKeyHex);
    }

    private static byte[] decodeMasterKey(String hex) {
        if (hex == null || hex.isBlank()) {
            throw new IllegalArgumentException(
                "ENCRYPTION_MASTER_KEY is not set. It must be exactly 256 bits (32 bytes) "
                    + "encoded as " + KEY_LENGTH_HEX + " hex characters. Generate one with: openssl rand -hex 32");
        }
        if (hex.length() != KEY_LENGTH_HEX) {
            throw new IllegalArgumentException(
                "ENCRYPTION_MASTER_KEY must be exactly 256 bits (32 bytes) encoded as "
                    + KEY_LENGTH_HEX + " hex characters, but was " + hex.length() + " characters.");
        }
        byte[] key = new byte[KEY_LENGTH_BYTE];
        for (int i = 0; i < KEY_LENGTH_HEX; i += 2) {
            int high = Character.digit(hex.charAt(i), 16);
            int low = Character.digit(hex.charAt(i + 1), 16);
            if (high < 0 || low < 0) {
                // Character.digit returns -1 for non-hex input; without this
                // check the key would silently decode to the wrong bytes.
                throw new IllegalArgumentException(
                    "ENCRYPTION_MASTER_KEY must be exactly 256 bits (32 bytes) encoded as "
                        + KEY_LENGTH_HEX + " hex characters, but contains a non-hexadecimal character at index "
                        + (high < 0 ? i : i + 1) + ".");
            }
            key[i / 2] = (byte) ((high << 4) + low);
        }
        return key;
    }

    public String encrypt(String plainText) {
        try {
            byte[] iv = new byte[IV_LENGTH_BYTE];
            SecureRandom random = new SecureRandom();
            random.nextBytes(iv);

            Cipher cipher = Cipher.getInstance(ALGORITHM);
            GCMParameterSpec parameterSpec = new GCMParameterSpec(TAG_LENGTH_BIT, iv);
            SecretKeySpec keySpec = new SecretKeySpec(masterKey, "AES");
            cipher.init(Cipher.ENCRYPT_MODE, keySpec, parameterSpec);

            byte[] cipherText = cipher.doFinal(plainText.getBytes(StandardCharsets.UTF_8));
            byte[] encryptedBuffer = new byte[IV_LENGTH_BYTE + cipherText.length];
            System.arraycopy(iv, 0, encryptedBuffer, 0, IV_LENGTH_BYTE);
            System.arraycopy(cipherText, 0, encryptedBuffer, IV_LENGTH_BYTE, cipherText.length);

            return Base64.getEncoder().encodeToString(encryptedBuffer);
        } catch (Exception e) {
            throw new RuntimeException("Error occurred during encryption", e);
        }
    }

    public String decrypt(String encryptedText) {
        try {
            byte[] encryptedBuffer = Base64.getDecoder().decode(encryptedText);
            byte[] iv = new byte[IV_LENGTH_BYTE];
            System.arraycopy(encryptedBuffer, 0, iv, 0, IV_LENGTH_BYTE);

            int cipherTextLength = encryptedBuffer.length - IV_LENGTH_BYTE;
            byte[] cipherText = new byte[cipherTextLength];
            System.arraycopy(encryptedBuffer, IV_LENGTH_BYTE, cipherText, 0, cipherTextLength);

            Cipher cipher = Cipher.getInstance(ALGORITHM);
            GCMParameterSpec parameterSpec = new GCMParameterSpec(TAG_LENGTH_BIT, iv);
            SecretKeySpec keySpec = new SecretKeySpec(masterKey, "AES");
            cipher.init(Cipher.DECRYPT_MODE, keySpec, parameterSpec);

            byte[] plainTextBytes = cipher.doFinal(cipherText);
            return new String(plainTextBytes, StandardCharsets.UTF_8);
        } catch (Exception e) {
            throw new RuntimeException("Error occurred during decryption", e);
        }
    }
}
