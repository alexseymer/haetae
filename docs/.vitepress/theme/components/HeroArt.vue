<script setup lang="ts">
// Original isometric wireframe: one control plane, three hosts, dashed outbound lines.
const U = 38 // px per unit
const OX = 300
const OY = 120
type P = [number, number, number]
const proj = ([x, y, z]: P): [number, number] => [OX + (x - y) * 0.866 * U, OY + (x + y) * 0.5 * U - z * U]
const pts = (ps: P[]) => ps.map((p) => proj(p).map((n) => n.toFixed(1)).join(',')).join(' ')

function box(x: number, y: number, z: number, w: number, d: number, h: number) {
  return {
    top: pts([[x, y, z + h], [x + w, y, z + h], [x + w, y + d, z + h], [x, y + d, z + h]]),
    left: pts([[x, y + d, z], [x + w, y + d, z], [x + w, y + d, z + h], [x, y + d, z + h]]),
    right: pts([[x + w, y, z], [x + w, y + d, z], [x + w, y + d, z + h], [x + w, y, z + h]]),
    // inner dividers along both visible faces
    divL: (t: number) => pts([[x, y + d, z + h * t], [x + w, y + d, z + h * t]]),
    divR: (t: number) => pts([[x + w, y, z + h * t], [x + w, y + d, z + h * t]])
  }
}

const cp = box(0, 0, 0, 4, 4, 1.9)
const cpLayers = [1 / 3, 2 / 3]
const cpTopGrid = [
  pts([[1.33, 0, 1.9], [1.33, 4, 1.9]]),
  pts([[2.66, 0, 1.9], [2.66, 4, 1.9]]),
  pts([[0, 1.33, 1.9], [4, 1.33, 1.9]]),
  pts([[0, 2.66, 1.9], [4, 2.66, 1.9]])
]

const hosts = [
  { name: 'hel1-prod', x: 6.6, y: 0.4, label: [0, 0] },
  { name: 'nbg-staging', x: 6.6, y: 4.9, label: [0, 0] },
  { name: 'home-lab', x: 0.5, y: 6.4, label: [0, 0] }
].map((h) => {
  const b = box(h.x, h.y, 0, 2.2, 2.2, 0.9)
  const minis = [
    box(h.x + 0.3, h.y + 0.3, 0.9, 0.6, 0.6, 0.5),
    box(h.x + 1.2, h.y + 0.3, 0.9, 0.6, 0.6, 0.5),
    box(h.x + 0.3, h.y + 1.2, 0.9, 0.6, 0.6, 0.5)
  ]
  const [lx, ly] = proj([h.x + 2.2, h.y + 2.2, 0])
  return { ...h, b, minis, lx, ly }
})

// Outbound connectors on the ground plane, host -> control plane (path direction = flow).
const lines = [
  { d: [[6.6, 1.5, 0], [4.05, 1.5, 0]] as P[] },
  { d: [[7.7, 4.9, 0], [7.7, 3.0, 0], [4.05, 3.0, 0]] as P[] },
  { d: [[1.6, 6.4, 0], [1.6, 4.05, 0]] as P[] }
].map((l) => ({
  path: 'M' + l.d.map((p) => proj(p).map((n) => n.toFixed(1)).join(' ')).join(' L')
}))

const [cpLx, cpLy] = proj([2, 4, 1.9])
const [cx, cy] = proj([2, 2, 1.9])
</script>

<template>
  <figure class="hx-art">
    <svg viewBox="0 0 600 520" role="img" aria-labelledby="hx-art-title hx-art-desc" preserveAspectRatio="xMidYMid meet">
      <title id="hx-art-title">Haetae architecture sketch</title>
      <desc id="hx-art-desc">
        One control plane block and three host blocks. Dashed lines run from each host to the control plane:
        hosts connect outbound only.
      </desc>
      <g class="hx-art__shape">
        <!-- control plane -->
        <g class="hx-art__block">
          <polygon :points="cp.left" class="f-left" />
          <polygon :points="cp.right" class="f-right" />
          <polygon :points="cp.top" class="f-top" />
          <polyline v-for="t in cpLayers" :key="'l' + t" :points="cp.divL(t)" class="hx-art__line" />
          <polyline v-for="t in cpLayers" :key="'r' + t" :points="cp.divR(t)" class="hx-art__line" />
          <polyline v-for="(g, i) in cpTopGrid" :key="'g' + i" :points="g" class="hx-art__line hx-art__line--faint" />
        </g>

        <!-- outbound connectors -->
        <path v-for="(l, i) in lines" :key="'c' + i" :d="l.path" class="hx-art__flow" />

        <!-- hosts -->
        <g v-for="h in hosts" :key="h.name" class="hx-art__block">
          <polygon :points="h.b.left" class="f-left" />
          <polygon :points="h.b.right" class="f-right" />
          <polygon :points="h.b.top" class="f-top" />
          <g v-for="(m, i) in h.minis" :key="i" class="hx-art__mini">
            <polygon :points="m.left" class="f-left" />
            <polygon :points="m.right" class="f-right" />
            <polygon :points="m.top" class="f-top" />
          </g>
        </g>
      </g>

      <!-- labels -->
      <g class="hx-art__labels" font-family="JetBrains Mono, ui-monospace, monospace" font-size="18" font-weight="700">
        <g :transform="`translate(${cx - 75} ${cy - 60})`">
          <rect width="150" height="30" fill="#0b1d3a" />
          <text x="75" y="21" text-anchor="middle" fill="#fff">control plane</text>
        </g>
        <g v-for="h in hosts" :key="'t' + h.name" :transform="`translate(${h.lx - 66} ${h.ly + 8})`">
          <rect width="132" height="28" fill="#0b1d3a" />
          <text x="66" y="20" text-anchor="middle" fill="#fff" font-size="16">{{ h.name }}</text>
        </g>
        <g transform="translate(24 484)">
          <rect width="236" height="28" fill="#0b1d3a" />
          <path d="M10 13h34" stroke="#8fb0ff" stroke-width="2.2" stroke-linecap="round" stroke-dasharray="3 6" />
          <text x="54" y="20" fill="#fff" font-size="15" font-weight="500">outbound only, mTLS</text>
        </g>
      </g>
    </svg>
  </figure>
</template>
