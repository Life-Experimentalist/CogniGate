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

    public EncryptionService(@Value("${ENCRYPTION_MASTER_KEY:0123456789abcdef0123456789abcdef}") String masterKeyHex) {
        // Decode hex string to bytes
        this.masterKey = hexStringToByteArray(masterKeyHex);
        if (this.masterKey.length != 32) {
            throw new IllegalArgumentException("ENCRYPTION_MASTER_KEY must be exactly 256 bits (32 bytes) long as a hex string.");
        }
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

    private static byte[] hexStringToByteArray(String s) {
        int len = s.length();
        byte[] data = new byte[len / 2];
        for (int i = 0; i < len; i += 2) {
            data[i / 2] = (byte) ((Character.digit(s.charAt(i), 16) << 4)
                                 + Character.digit(s.charAt(i+1), 16));
        }
        return data;
    }
}
