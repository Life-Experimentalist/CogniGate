package com.cognigate.config;

import jakarta.servlet.FilterChain;
import jakarta.servlet.ServletException;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.web.filter.OncePerRequestFilter;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;

/**
 * ApiTokenFilter is the whole of the authentication on {@code /api/**}.
 *
 * <p>The compose deployment publishes this service on a host port, and
 * {@code POST /api/v1/usage} writes the rows every invoice is computed from
 * while {@code /api/admin} mints tenant credentials. Both are one shared
 * secret away from anyone who can reach the port, and that secret is this one.
 *
 * <p>It is a filter rather than an authentication provider because there is no
 * principal here to authenticate — one process is proving to another that they
 * were configured together. Refusing in the filter and leaving the request
 * unauthenticated keeps the chain rules readable: what reaches the controllers
 * has already presented the token.
 */
final class ApiTokenFilter extends OncePerRequestFilter {

    private static final String SCHEME = "Bearer ";
    private static final String GUARDED = "/api/";

    private final byte[] token;

    ApiTokenFilter(String token) {
        this.token = token.getBytes(StandardCharsets.UTF_8);
    }

    @Override
    protected boolean shouldNotFilter(HttpServletRequest request) {
        return !path(request).startsWith(GUARDED);
    }

    @Override
    protected void doFilterInternal(HttpServletRequest request, HttpServletResponse response, FilterChain chain)
            throws ServletException, IOException {
        String header = request.getHeader(HttpHeaders.AUTHORIZATION);
        if (header == null || !header.startsWith(SCHEME) || !matches(header.substring(SCHEME.length()))) {
            refuse(response);
            return;
        }
        chain.doFilter(request, response);
    }

    /**
     * Compares in constant time. {@link String#equals} returns at the first
     * differing byte, which hands a caller who can measure the difference the
     * token one character at a time.
     */
    private boolean matches(String presented) {
        return MessageDigest.isEqual(presented.getBytes(StandardCharsets.UTF_8), token);
    }

    /**
     * The request path without the context path, so a service deployed under a
     * prefix still guards the same endpoints.
     */
    private static String path(HttpServletRequest request) {
        return request.getRequestURI().substring(request.getContextPath().length());
    }

    /**
     * The body says which variable is wrong, because the caller here is another
     * process and the operator reading its logs has two of them to reconcile.
     * It does not distinguish a missing token from a wrong one: that difference
     * is worth nothing to a legitimate caller and something to an attacker.
     */
    private static void refuse(HttpServletResponse response) throws IOException {
        response.setStatus(HttpStatus.UNAUTHORIZED.value());
        response.setContentType(MediaType.APPLICATION_JSON_VALUE);
        response.getWriter().write(
                "{\"error\":\"A valid 'Authorization: Bearer <ANALYTICS_TOKEN>' header is required.\"}");
    }
}
