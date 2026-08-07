"use client";

import { useRef, useEffect, useMemo } from "react";
import * as THREE from "three";

interface Particle {
  position: THREE.Vector3;
  velocity: THREE.Vector3;
  connections: number[];
}

export default function ParticleNetwork() {
  const mountRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!mountRef.current) return;

    const mount = mountRef.current;
    const width = mount.clientWidth;
    const height = mount.clientHeight;

    // --- Scene Setup ---
    const scene = new THREE.Scene();
    const camera = new THREE.PerspectiveCamera(60, width / height, 0.1, 1000);
    camera.position.z = 80;

    const renderer = new THREE.WebGLRenderer({ antialias: true, alpha: true });
    renderer.setSize(width, height);
    renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
    renderer.setClearColor(0x000000, 0);
    mount.appendChild(renderer.domElement);

    // --- Particles ---
    const PARTICLE_COUNT = 120;
    const CONNECTION_DISTANCE = 25;
    const particles: Particle[] = [];

    const positions = new Float32Array(PARTICLE_COUNT * 3);
    const colors = new Float32Array(PARTICLE_COUNT * 3);

    const cyanColor = new THREE.Color(0x06b6d4);
    const violetColor = new THREE.Color(0x7c3aed);
    const emeraldColor = new THREE.Color(0x10b981);
    const palette = [cyanColor, violetColor, emeraldColor];

    for (let i = 0; i < PARTICLE_COUNT; i++) {
      const x = (Math.random() - 0.5) * 160;
      const y = (Math.random() - 0.5) * 100;
      const z = (Math.random() - 0.5) * 60;

      particles.push({
        position: new THREE.Vector3(x, y, z),
        velocity: new THREE.Vector3(
          (Math.random() - 0.5) * 0.04,
          (Math.random() - 0.5) * 0.04,
          (Math.random() - 0.5) * 0.02,
        ),
        connections: [],
      });

      positions[i * 3] = x;
      positions[i * 3 + 1] = y;
      positions[i * 3 + 2] = z;

      const col = palette[Math.floor(Math.random() * palette.length)];
      colors[i * 3] = col.r;
      colors[i * 3 + 1] = col.g;
      colors[i * 3 + 2] = col.b;
    }

    const particleGeometry = new THREE.BufferGeometry();
    particleGeometry.setAttribute("position", new THREE.BufferAttribute(positions, 3));
    particleGeometry.setAttribute("color", new THREE.BufferAttribute(colors, 3));

    const particleMaterial = new THREE.PointsMaterial({
      size: 1.5,
      vertexColors: true,
      transparent: true,
      opacity: 0.9,
      sizeAttenuation: true,
    });

    const pointCloud = new THREE.Points(particleGeometry, particleMaterial);
    scene.add(pointCloud);

    // --- Connection Lines ---
    const MAX_CONNECTIONS = PARTICLE_COUNT * 8;
    const linePositions = new Float32Array(MAX_CONNECTIONS * 6);
    const lineColors = new Float32Array(MAX_CONNECTIONS * 6);
    let lineCount = 0;

    const lineGeometry = new THREE.BufferGeometry();
    lineGeometry.setAttribute("position", new THREE.BufferAttribute(linePositions, 3));
    lineGeometry.setAttribute("color", new THREE.BufferAttribute(lineColors, 3));

    const lineMaterial = new THREE.LineSegments(
      lineGeometry,
      new THREE.LineBasicMaterial({ vertexColors: true, transparent: true, opacity: 0.25 }),
    );
    scene.add(lineMaterial);

    // --- Mouse Interaction ---
    const mouse = new THREE.Vector2(0, 0);
    const onMouseMove = (e: MouseEvent) => {
      mouse.x = (e.clientX / window.innerWidth) * 2 - 1;
      mouse.y = -(e.clientY / window.innerHeight) * 2 + 1;
    };
    window.addEventListener("mousemove", onMouseMove);

    // --- Animation ---
    let animationId: number;
    const animate = () => {
      animationId = requestAnimationFrame(animate);

      // Subtle camera sway on mouse
      camera.position.x += (mouse.x * 8 - camera.position.x) * 0.01;
      camera.position.y += (mouse.y * 5 - camera.position.y) * 0.01;
      camera.lookAt(scene.position);

      // Update particle positions
      lineCount = 0;
      for (let i = 0; i < PARTICLE_COUNT; i++) {
        const p = particles[i];
        p.position.add(p.velocity);

        // Bounce off boundaries
        if (Math.abs(p.position.x) > 80) p.velocity.x *= -1;
        if (Math.abs(p.position.y) > 50) p.velocity.y *= -1;
        if (Math.abs(p.position.z) > 30) p.velocity.z *= -1;

        positions[i * 3] = p.position.x;
        positions[i * 3 + 1] = p.position.y;
        positions[i * 3 + 2] = p.position.z;

        // Build connections
        for (let j = i + 1; j < PARTICLE_COUNT; j++) {
          const dist = p.position.distanceTo(particles[j].position);
          if (dist < CONNECTION_DISTANCE && lineCount < MAX_CONNECTIONS) {
            const opacity = 1 - dist / CONNECTION_DISTANCE;
            const idx = lineCount * 6;

            linePositions[idx] = p.position.x;
            linePositions[idx + 1] = p.position.y;
            linePositions[idx + 2] = p.position.z;
            linePositions[idx + 3] = particles[j].position.x;
            linePositions[idx + 4] = particles[j].position.y;
            linePositions[idx + 5] = particles[j].position.z;

            // Blend from cyan to violet based on particle index
            const t = i / PARTICLE_COUNT;
            const r = cyanColor.r * (1 - t) + violetColor.r * t;
            const g = cyanColor.g * (1 - t) + violetColor.g * t;
            const b = cyanColor.b * (1 - t) + violetColor.b * t;

            lineColors[idx] = r * opacity;
            lineColors[idx + 1] = g * opacity;
            lineColors[idx + 2] = b * opacity;
            lineColors[idx + 3] = r * opacity;
            lineColors[idx + 4] = g * opacity;
            lineColors[idx + 5] = b * opacity;

            lineCount++;
          }
        }
      }

      // Slow global rotation
      pointCloud.rotation.y += 0.0008;
      lineMaterial.rotation.y += 0.0008;

      (particleGeometry.attributes.position as THREE.BufferAttribute).needsUpdate = true;
      (lineGeometry.attributes.position as THREE.BufferAttribute).needsUpdate = true;
      (lineGeometry.attributes.color as THREE.BufferAttribute).needsUpdate = true;
      lineGeometry.setDrawRange(0, lineCount * 2);

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
      window.removeEventListener("resize", onResize);
      renderer.dispose();
      if (mount.contains(renderer.domElement)) {
        mount.removeChild(renderer.domElement);
      }
    };
  }, []);

  return (
    <div
      ref={mountRef}
      style={{
        position: "absolute",
        inset: 0,
        overflow: "hidden",
        zIndex: 0,
        pointerEvents: "none",
      }}
    />
  );
}
