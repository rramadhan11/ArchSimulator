import {
  Background,
  BackgroundVariant,
  Controls,
  MarkerType,
  MiniMap,
  ReactFlow,
  useReactFlow,
  type Connection,
  type IsValidConnection,
  type XYPosition,
} from '@xyflow/react'
import { useCallback, useMemo, type DragEvent } from 'react'
import { dropTarget, useDesign } from '../store/design'
import { archetypeOf, isCallable, type FlowEdge, type FlowNode } from '../store/model'
import { archetypeMeta } from './archetypes'
import { nodeTypes } from './nodes'

export const DRAG_MIME = 'application/x-archsim-kind'

function edgeLabel(e: FlowEdge): string {
  const d = e.data ?? {}
  const parts = [d.protocol]
  if (d.op) parts.push(d.op)
  if (d.when) parts.push(d.when.replace('_', ' '))
  else if (d.probability !== undefined) parts.push(`p=${d.probability}`)
  if (d.callsPerRequest !== undefined && d.callsPerRequest !== 1) parts.push(`×${d.callsPerRequest}`)
  if (d.classes?.length) parts.push(`[${d.classes.join(',')}]`)
  if (d.parallelGroup) parts.push(`∥${d.parallelGroup}`)
  return parts.filter(Boolean).join(' · ')
}

export function Canvas() {
  const nodes = useDesign((s) => s.nodes)
  const edges = useDesign((s) => s.edges)
  const selection = useDesign((s) => s.selection)
  const catalog = useDesign((s) => s.catalog)
  const { onNodesChange, onEdgesChange, select, connect, addNode, reparent } = useDesign.getState()
  const rf = useReactFlow<FlowNode, FlowEdge>()

  // Selection lives in the store (so panels can select too); mirror it here.
  const viewNodes = useMemo(
    () => nodes.map((n) => ({ ...n, selected: selection?.type === 'node' && selection.id === n.id })),
    [nodes, selection],
  )
  const viewEdges = useMemo(
    () =>
      edges.map(
        (e): FlowEdge => ({
          ...e,
          label: edgeLabel(e),
          selected: selection?.type === 'edge' && selection.id === e.id,
          animated: !!e.data?.async,
          markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16 },
          style: e.data?.async ? { strokeDasharray: '5 4' } : undefined,
        }),
      ),
    [edges, selection],
  )

  /** Containers whose box contains the point, outermost first. */
  const containersAt = useCallback(
    (p: XYPosition, excludeId?: string): FlowNode[] =>
      rf.getNodes().filter((n) => {
        if (n.type !== 'container' || n.id === excludeId) return false
        const internal = rf.getInternalNode(n.id)
        if (!internal) return false
        const { x, y } = internal.internals.positionAbsolute
        const w = n.width ?? n.measured?.width ?? 0
        const h = n.height ?? n.measured?.height ?? 0
        return p.x >= x && p.x <= x + w && p.y >= y && p.y <= y + h
      }),
    [rf],
  )

  const relativeTo = (parentId: string | undefined, abs: XYPosition): XYPosition => {
    if (!parentId) return abs
    const p = rf.getInternalNode(parentId)!.internals.positionAbsolute
    return { x: abs.x - p.x, y: abs.y - p.y }
  }

  const onDrop = (ev: DragEvent) => {
    const kind = ev.dataTransfer.getData(DRAG_MIME)
    if (!kind) return
    ev.preventDefault()
    const at = rf.screenToFlowPosition({ x: ev.clientX, y: ev.clientY })
    const parent = dropTarget(kind, containersAt(at))
    addNode(kind, relativeTo(parent?.id, at), parent?.id)
  }

  const onNodeDragStop = (_: unknown, node: FlowNode) => {
    const internal = rf.getInternalNode(node.id)
    if (!internal) return
    const abs = internal.internals.positionAbsolute
    const center = { x: abs.x + (node.measured?.width ?? 0) / 2, y: abs.y + (node.measured?.height ?? 0) / 2 }
    const parent = dropTarget(node.data.kind, containersAt(center, node.id), node.id)
    if (parent?.id === node.parentId) return
    // Never nest a container inside its own descendant.
    for (let p = parent; p; p = p.parentId ? rf.getNode(p.parentId) : undefined) if (p.id === node.id) return
    reparent(node.id, parent?.id, relativeTo(parent?.id, abs))
  }

  const isValidConnection: IsValidConnection = (c) => {
    if (c.source === c.target) return false
    const kind = (id: string) => rf.getNode(id)?.data.kind ?? ''
    return isCallable(archetypeOf(catalog, kind(c.source))) && isCallable(archetypeOf(catalog, kind(c.target)))
  }

  return (
    <ReactFlow<FlowNode, FlowEdge>
      nodes={viewNodes}
      edges={viewEdges}
      nodeTypes={nodeTypes}
      onNodesChange={(changes) => onNodesChange(changes.filter((c) => c.type !== 'select'))}
      onEdgesChange={(changes) => onEdgesChange(changes.filter((c) => c.type !== 'select'))}
      onConnect={(c: Connection) => connect(c.source, c.target)}
      isValidConnection={isValidConnection}
      onNodeClick={(_, n) => select({ type: 'node', id: n.id })}
      onEdgeClick={(_, e) => select({ type: 'edge', id: e.id })}
      onPaneClick={() => select(null)}
      onNodeDragStart={(_, n) => select({ type: 'node', id: n.id })}
      onNodeDragStop={onNodeDragStop}
      onDragOver={(ev) => {
        if (ev.dataTransfer.types.includes(DRAG_MIME)) {
          ev.preventDefault()
          ev.dataTransfer.dropEffect = 'copy'
        }
      }}
      onDrop={onDrop}
      deleteKeyCode={['Backspace', 'Delete']}
      fitView
      fitViewOptions={{ padding: 0.2, maxZoom: 1 }}
      minZoom={0.2}
      proOptions={{ hideAttribution: false }}
    >
      <Background variant={BackgroundVariant.Dots} gap={18} size={1.2} color="var(--canvas-dot)" />
      <Controls showInteractive={false} position="bottom-left" />
      <MiniMap
        pannable
        zoomable
        position="bottom-right"
        nodeColor={(n) => {
          const a = archetypeOf(catalog, (n as FlowNode).data.kind)
          return a ? archetypeMeta[a].color : '#888'
        }}
        nodeStrokeWidth={0}
        bgColor="var(--panel)"
        maskColor="color-mix(in srgb, var(--bg) 70%, transparent)"
        style={{ width: 160, height: 100 }}
      />
    </ReactFlow>
  )
}
