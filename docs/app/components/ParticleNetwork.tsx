"use client";

import { useRef, useEffect, useState } from "react";
import * as THREE from "three";

interface SimPacket {
    progress: number;
    speed: number;
    stage: "to_gateway" | "to_provider";
    targetProvider: "openai" | "anthropic" | "custom";
    position: THREE.Vector3;
}

export default function ParticleNetwork() {
    const mountRef = useRef<HTMLDivElement>(null);
    const [hudText, setHudText] = useState(
        "Status: Monitoring Route. Primary: OpenAI",
    );
    const [hudColor, setHudColor] = useState("#06b6d4");
    // A ref rather than state: the only readers are inside the animation
    // effect below, which mounts once. As state it was captured stale, so
    // losing the network never actually reached the running animation —
    // and adding it to that effect's deps would rebuild the whole scene
    // on every network flap.
    const isOfflineRef = useRef(false);

    useEffect(() => {
        const handleOffline = () => {
            isOfflineRef.current = true;
        };
        const handleOnline = () => {
            isOfflineRef.current = false;
        };

        // Check initial state
        if (typeof navigator !== "undefined" && !navigator.onLine) {
            isOfflineRef.current = true;
        }

        window.addEventListener("offline", handleOffline);
        window.addEventListener("online", handleOnline);
        return () => {
            window.removeEventListener("offline", handleOffline);
            window.removeEventListener("online", handleOnline);
        };
    }, []);

    useEffect(() => {
        if (!mountRef.current) return;

        const mount = mountRef.current;
        const width = mount.clientWidth;
        const height = mount.clientHeight;

        // --- Scene Setup ---
        const scene = new THREE.Scene();
        const camera = new THREE.PerspectiveCamera(
            60,
            width / height,
            0.1,
            1000,
        );
        camera.position.set(0, 0, 75);

        const renderer = new THREE.WebGLRenderer({
            antialias: true,
            alpha: true,
        });
        renderer.setSize(width, height);
        renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
        renderer.setClearColor(0x000000, 0);
        mount.appendChild(renderer.domElement);

        // --- Core Nodes Setup ---
        // 1. Client Hub (Left)
        const clientPos = new THREE.Vector3(-25, 0, 0);
        // 2. Gateway Core (Center)
        const gatewayPos = new THREE.Vector3(0, 0, 0);
        // 3. OpenAI Node (Top Right)
        const openaiPos = new THREE.Vector3(25, 12, 0);
        // 4. Anthropic Node (Middle Right)
        const anthropicPos = new THREE.Vector3(25, 0, 0);
        // 5. Custom Node (Bottom Right)
        const customPos = new THREE.Vector3(25, -12, 0);

        // Node meshes
        const nodeGeometry = new THREE.SphereGeometry(1.4, 32, 32);

        const clientMat = new THREE.MeshBasicMaterial({ color: 0x06b6d4 });
        const gatewayMat = new THREE.MeshBasicMaterial({ color: 0x10b981 });
        const openaiMat = new THREE.MeshBasicMaterial({ color: 0x06b6d4 });
        const anthropicMat = new THREE.MeshBasicMaterial({ color: 0xf59e0b });
        const customMat = new THREE.MeshBasicMaterial({ color: 0x7c3aed });

        const clientMesh = new THREE.Mesh(nodeGeometry, clientMat);
        clientMesh.position.copy(clientPos);
        scene.add(clientMesh);

        const gatewayMesh = new THREE.Mesh(nodeGeometry, gatewayMat);
        gatewayMesh.position.copy(gatewayPos);
        scene.add(gatewayMesh);

        const openaiMesh = new THREE.Mesh(nodeGeometry, openaiMat);
        openaiMesh.position.copy(openaiPos);
        scene.add(openaiMesh);

        const anthropicMesh = new THREE.Mesh(nodeGeometry, anthropicMat);
        anthropicMesh.position.copy(anthropicPos);
        scene.add(anthropicMesh);

        const customMesh = new THREE.Mesh(nodeGeometry, customMat);
        customMesh.position.copy(customPos);
        scene.add(customMesh);

        // Router Torus Ring around the Gateway
        const torusGeom = new THREE.TorusGeometry(3.5, 0.4, 16, 100);
        const torusMat = new THREE.MeshBasicMaterial({
            color: 0x10b981,
            wireframe: true,
            transparent: true,
            opacity: 0.6,
        });
        const torusMesh = new THREE.Mesh(torusGeom, torusMat);
        torusMesh.position.copy(gatewayPos);
        scene.add(torusMesh);

        // --- Background Stars/Noise Cloud ---
        const BG_PARTICLES = 80;
        const bgGeometry = new THREE.BufferGeometry();
        const bgPositions = new Float32Array(BG_PARTICLES * 3);
        const bgColors = new Float32Array(BG_PARTICLES * 3);

        for (let i = 0; i < BG_PARTICLES; i++) {
            bgPositions[i * 3] = (Math.random() - 0.5) * 150;
            bgPositions[i * 3 + 1] = (Math.random() - 0.5) * 80;
            bgPositions[i * 3 + 2] = (Math.random() - 0.5) * 60;

            // Faint ambient colors
            const r = 0.05 + Math.random() * 0.08;
            const g = 0.08 + Math.random() * 0.12;
            const b = 0.15 + Math.random() * 0.15;
            bgColors[i * 3] = r;
            bgColors[i * 3 + 1] = g;
            bgColors[i * 3 + 2] = b;
        }

        bgGeometry.setAttribute(
            "position",
            new THREE.BufferAttribute(bgPositions, 3),
        );
        bgGeometry.setAttribute(
            "color",
            new THREE.BufferAttribute(bgColors, 3),
        );

        // Glow dot texture
        const canvas = document.createElement("canvas");
        canvas.width = 16;
        canvas.height = 16;
        const ctx = canvas.getContext("2d");
        if (ctx) {
            const grad = ctx.createRadialGradient(8, 8, 0, 8, 8, 8);
            grad.addColorStop(0, "rgba(255,255,255,1)");
            grad.addColorStop(1, "rgba(255,255,255,0)");
            ctx.fillStyle = grad;
            ctx.fillRect(0, 0, 16, 16);
        }
        const dotTexture = new THREE.CanvasTexture(canvas);

        const bgMaterial = new THREE.PointsMaterial({
            size: 1.8,
            vertexColors: true,
            transparent: true,
            opacity: 0.6,
            map: dotTexture,
            depthWrite: false,
        });
        const bgPoints = new THREE.Points(bgGeometry, bgMaterial);
        scene.add(bgPoints);

        // --- Routing Link Lines ---
        const lineMat = new THREE.LineBasicMaterial({
            color: 0x1f2937,
            transparent: true,
            opacity: 0.35,
        });

        const createLine = (p1: THREE.Vector3, p2: THREE.Vector3) => {
            const geom = new THREE.BufferGeometry().setFromPoints([p1, p2]);
            return new THREE.Line(geom, lineMat);
        };

        const clientToGateLine = createLine(clientPos, gatewayPos);
        const gateToOpenaiLine = createLine(gatewayPos, openaiPos);
        const gateToAnthropicLine = createLine(gatewayPos, anthropicPos);
        const gateToCustomLine = createLine(gatewayPos, customPos);

        scene.add(clientToGateLine);
        scene.add(gateToOpenaiLine);
        scene.add(gateToAnthropicLine);
        scene.add(gateToCustomLine);

        // --- Dynamic Neon Packets ---
        const PACKET_COUNT = 8;
        const packets: SimPacket[] = [];

        // Packet particle meshes
        const packetGeometry = new THREE.SphereGeometry(0.5, 16, 16);
        const packetMeshes: THREE.Mesh[] = [];

        for (let i = 0; i < PACKET_COUNT; i++) {
            const packetMat = new THREE.MeshBasicMaterial({ color: 0x06b6d4 });
            const mesh = new THREE.Mesh(packetGeometry, packetMat);
            scene.add(mesh);
            packetMeshes.push(mesh);

            packets.push({
                progress: Math.random(),
                speed: 0.01 + Math.random() * 0.008,
                stage: Math.random() > 0.5 ? "to_gateway" : "to_provider",
                targetProvider: "openai",
                position: new THREE.Vector3().copy(clientPos),
            });
        }

        // --- Interaction States ---
        const mouse = new THREE.Vector2(0, 0);
        const onMouseMove = (e: MouseEvent) => {
            mouse.x = (e.clientX / window.innerWidth) * 2 - 1;
            mouse.y = -(e.clientY / window.innerHeight) * 2 + 1;
        };
        window.addEventListener("mousemove", onMouseMove);

        let scrollY = 0;
        const onScroll = () => {
            scrollY = window.scrollY;
        };
        window.addEventListener("scroll", onScroll);

        // --- Animation State Machine ---
        let stateTime = 0;
        let currentState: 0 | 1 | 2 | 3 | 4 | 5 = 0;

        let animationId: number;
        let time = 0;

        const animate = () => {
            animationId = requestAnimationFrame(animate);
            time += 0.005;
            stateTime += 0.016; // Approx delta-time

            // State machine logic
            // State 0: OpenAI Healthy (0s - 6s)
            // State 1: OpenAI Outage (6s - 8s)
            // State 2: Failover to Anthropic (8s - 14s)
            // State 3: Anthropic Outage (14s - 16s)
            // State 4: Failover to Custom Provider (16s - 22s)
            // State 5: System Self-Healing & Recovery (22s - 24s)
            if (stateTime > 24) {
                stateTime = 0;
                currentState = 0;
            } else if (stateTime > 22) {
                currentState = 5;
            } else if (stateTime > 16) {
                currentState = 4;
            } else if (stateTime > 14) {
                currentState = 3;
            } else if (stateTime > 8) {
                currentState = 2;
            } else if (stateTime > 6) {
                currentState = 1;
            }

            // Update Node Colors based on State
            let activeProvider: "openai" | "anthropic" | "custom" = "openai";

            if (isOfflineRef.current) {
                setHudText(
                    "CRITICAL: User is Offline. All connections lost. Waiting for network...",
                );
                setHudColor("#ef4444");
                openaiMat.color.setHex(0x7f1d1d);
                anthropicMat.color.setHex(0x7f1d1d);
                customMat.color.setHex(0x7f1d1d);
                clientMat.color.setHex(0x7f1d1d);
                gatewayMat.color.setHex(0x7f1d1d);
            } else if (currentState === 0) {
                clientMat.color.setHex(0x06b6d4);
                gatewayMat.color.setHex(0x10b981);
                setHudText("Active Route: OpenAI (Status 200 OK — Primary)");
                setHudColor("#06b6d4");
                openaiMat.color.setHex(0x10b981); // Green
                anthropicMat.color.setHex(0x4b5563); // Muted
                customMat.color.setHex(0x4b5563); // Muted
                activeProvider = "openai";
            } else if (currentState === 1) {
                setHudText(
                    "CRITICAL: OpenAI Failed (503 Service Unavailable) — Tripping Circuit Breaker!",
                );
                setHudColor("#ef4444");
                // Flash OpenAI red
                const flash = Math.sin(time * 30) > 0;
                openaiMat.color.setHex(flash ? 0xef4444 : 0x7f1d1d);
                anthropicMat.color.setHex(0x4b5563);
                customMat.color.setHex(0x4b5563);
                activeProvider = "openai";
            } else if (currentState === 2) {
                setHudText(
                    "FAILOVER ACTIVE: Rerouting Traffic to Anthropic (Backup Model)",
                );
                setHudColor("#f59e0b");
                openaiMat.color.setHex(0x7f1d1d); // Offline Red
                anthropicMat.color.setHex(0x10b981); // Active Green
                customMat.color.setHex(0x4b5563); // Muted
                activeProvider = "anthropic";
            } else if (currentState === 3) {
                setHudText(
                    "CRITICAL: Anthropic Failed (429 Rate Limited) — Cascading Routing Rules!",
                );
                setHudColor("#ef4444");
                openaiMat.color.setHex(0x7f1d1d);
                const flash = Math.sin(time * 30) > 0;
                anthropicMat.color.setHex(flash ? 0xef4444 : 0x7f1d1d);
                customMat.color.setHex(0x4b5563);
                activeProvider = "anthropic";
            } else if (currentState === 4) {
                setHudText(
                    "SECONDARY FAILOVER: Routing Traffic to Custom Model (On-Premises)",
                );
                setHudColor("#7c3aed");
                openaiMat.color.setHex(0x7f1d1d);
                anthropicMat.color.setHex(0x7f1d1d);
                customMat.color.setHex(0x10b981); // Active Green
                activeProvider = "custom";
            } else if (currentState === 5) {
                setHudText(
                    "RECOVERY: Downstream Providers Restored. Re-incorporating Key Pools.",
                );
                setHudColor("#10b981");
                openaiMat.color.setHex(0x06b6d4);
                anthropicMat.color.setHex(0xf59e0b);
                customMat.color.setHex(0x7c3aed);
            }

            // Rotate Torus Core
            torusMesh.rotation.y += 0.02;
            torusMesh.rotation.x += 0.01;

            // Update Packets movement
            for (let i = 0; i < PACKET_COUNT; i++) {
                const p = packets[i];
                const mesh = packetMeshes[i];

                // Skip packets if in failure transition state (States 1 or 3) or Offline
                if (
                    isOfflineRef.current ||
                    currentState === 1 ||
                    currentState === 3
                ) {
                    // Pulse the packet out of existence or fade opacity
                    mesh.scale.set(0, 0, 0);
                    continue;
                } else {
                    mesh.scale.set(1, 1, 1);
                }

                p.progress += p.speed;
                if (p.progress >= 1.0) {
                    p.progress = 0.0;
                    if (p.stage === "to_gateway") {
                        p.stage = "to_provider";
                        p.targetProvider = activeProvider;
                    } else {
                        p.stage = "to_gateway";
                    }
                }

                // Interpolate position
                if (p.stage === "to_gateway") {
                    mesh.position.lerpVectors(
                        clientPos,
                        gatewayPos,
                        p.progress,
                    );
                    (mesh.material as THREE.MeshBasicMaterial).color.setHex(
                        0x06b6d4,
                    ); // Cyan incoming
                } else {
                    // Route to active provider
                    const targetPos =
                        p.targetProvider === "openai"
                            ? openaiPos
                            : p.targetProvider === "anthropic"
                              ? anthropicPos
                              : customPos;

                    mesh.position.lerpVectors(
                        gatewayPos,
                        targetPos,
                        p.progress,
                    );

                    // Color reflects provider selection
                    const packetColor =
                        p.targetProvider === "openai"
                            ? 0x06b6d4
                            : p.targetProvider === "anthropic"
                              ? 0xf59e0b
                              : 0x7c3aed;
                    (mesh.material as THREE.MeshBasicMaterial).color.setHex(
                        packetColor,
                    );
                }
            }

            // --- Camera Movement (Scrolling and Mouse) ---
            // Rotate the camera around the Y axis and move down Z based on scroll
            const zDepth = 75 - scrollY * 0.05;
            const targetCamX = Math.sin(time * 0.15) * 4 + mouse.x * 10;
            const targetCamY =
                Math.cos(time * 0.1) * 3 + mouse.y * 6 - scrollY * 0.02;

            camera.position.x += (targetCamX - camera.position.x) * 0.03;
            camera.position.y += (targetCamY - camera.position.y) * 0.03;
            camera.position.z += (zDepth - camera.position.z) * 0.05;
            camera.lookAt(0, -scrollY * 0.015, 0);

            // Faint rotation of background star field
            bgPoints.rotation.y += 0.0003;

            renderer.render(scene, camera);
        };

        animate();

        // --- Resize Handler ---
        const onResize = () => {
            const w = mount.clientWidth;
            const h = mount.clientHeight;
            camera.aspect = w / h;
            camera.updateProjectionMatrix();
            renderer.setSize(w, h);
        };
        window.addEventListener("resize", onResize);

        return () => {
            cancelAnimationFrame(animationId);
            window.removeEventListener("mousemove", onMouseMove);
            window.removeEventListener("scroll", onScroll);
            window.removeEventListener("resize", onResize);
            renderer.dispose();
            if (mount.contains(renderer.domElement)) {
                mount.removeChild(renderer.domElement);
            }
        };
    }, []);

    return (
        <div
            style={{
                position: "absolute",
                inset: 0,
                overflow: "hidden",
                zIndex: 0,
                pointerEvents: "none",
            }}
        >
            <div ref={mountRef} style={{ width: "100%", height: "100%" }} />

            {/* Floating Simulation HUD Overlay */}
            <div
                style={{
                    position: "absolute",
                    bottom: "40px",
                    left: "50%",
                    transform: "translateX(-50%)",
                    background: "rgba(10, 14, 26, 0.7)",
                    backdropFilter: "blur(12px)",
                    border: `1px solid ${hudColor}33`,
                    borderRadius: "12px",
                    padding: "10px 24px",
                    display: "flex",
                    alignItems: "center",
                    gap: "12px",
                    boxShadow: `0 8px 32px 0 rgba(0, 0, 0, 0.4), 0 0 15px ${hudColor}1a`,
                    transition: "all 0.3s ease",
                    zIndex: 10,
                }}
            >
                <span
                    style={{
                        width: "8px",
                        height: "8px",
                        borderRadius: "50%",
                        backgroundColor: hudColor,
                        boxShadow: `0 0 8px ${hudColor}`,
                        display: "inline-block",
                        animation: "pulse 1.5s infinite",
                    }}
                />
                <span
                    style={{
                        fontFamily: "monospace",
                        fontSize: "12px",
                        fontWeight: 600,
                        color: "#e2e8f0",
                        letterSpacing: "0.2px",
                    }}
                >
                    {hudText}
                </span>
            </div>
        </div>
    );
}
