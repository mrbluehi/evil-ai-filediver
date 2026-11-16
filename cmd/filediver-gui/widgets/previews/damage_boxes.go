package previews

import (
	"fmt"
	"math"
	"strconv"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/go-gl/gl/v4.3-core/gl"
	"github.com/go-gl/mathgl/mgl32"
	preview_mats "github.com/xypwn/filediver/cmd/filediver-gui/widgets/previews/materials"
	datalibrary "github.com/xypwn/filediver/datalibrary"
	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/physics"
)

type damageOverlayBox struct {
	actor stingray.ThinHash
	bone  int
	min   mgl32.Vec3
	max   mgl32.Vec3
}

type damageOverlayVolume struct {
	name     string
	shape    datalibrary.DamageDealerZoneShape
	bone     int
	size     mgl32.Vec3
	position mgl32.Vec3
	rotation mgl32.Vec3
	mode     datalibrary.OverlapDamageMode
	damage   [5]float32
}

func (pv *UnitPreviewState) setDamageOverlayToggle(toggle *bool, enabled bool) {
	if enabled {
		if err := pv.loadDamageOverlayData(); err != nil {
			pv.damageOverlayError = err.Error()
			*toggle = false
			return
		}
	}
	*toggle = enabled
}

// Draw uses this same preview state for the Preview and Animations tabs.
func (pv *UnitPreviewState) drawDamageOverlayControls() {
	eligible := pv.rootHash.Type == UNIT && !pv.isHelldivers1
	imgui.BeginDisabledV(!eligible)

	hitboxes := pv.showPhysicsHitboxes
	if imgui.Checkbox("Hitboxes (blue)", &hitboxes) {
		pv.setDamageOverlayToggle(&pv.showPhysicsHitboxes, hitboxes)
	}
	imgui.SetItemTooltip("Show Havok collision actor bounds in blue. These are hitboxes, not attack damage volumes.")

	imgui.SameLine()
	damageBoxes := pv.showDamageBoxes
	if imgui.Checkbox("Damage boxes (red)", &damageBoxes) {
		pv.setDamageOverlayToggle(&pv.showDamageBoxes, damageBoxes)
	}
	damageTooltip := "Show authored OverlapDamageComponent shapes in red. Their position follows the named skeleton bone during animation; no physics-bound fallback is drawn."
	if pv.damageOverlayLoaded {
		damageTooltip += fmt.Sprintf("\nAuthored regions found: %d", len(pv.damageOverlayVolumes))
	}
	if pv.damageOverlayError != "" {
		damageTooltip += "\n" + pv.damageOverlayError
	}
	imgui.SetItemTooltip(damageTooltip)

	imgui.SameLine()
	readout := pv.showDamageReadout
	if imgui.Checkbox("Readout", &readout) {
		pv.setDamageOverlayToggle(&pv.showDamageReadout, readout)
	}
	imgui.SetItemTooltip("Show the authored region name, actual shape, dimensions, and overlap-damage values. Armor penetration is not stored in this zone component, so no AP value is invented.")

	if !eligible {
		imgui.SetItemTooltip("Select a Helldivers 2 unit to inspect its authored damage volumes.")
	}
	imgui.EndDisabled()
}

func (pv *UnitPreviewState) loadDamageOverlayData() error {
	if pv.damageOverlayLoaded && pv.damageOverlayUnit == pv.rootHash {
		return nil
	}
	if pv.rootHash.Type != UNIT || pv.isHelldivers1 || pv.rootUnitInfo == nil {
		return fmt.Errorf("damage overlays require a loaded Helldivers 2 unit skeleton")
	}
	pv.damageOverlayBoxes = nil
	pv.damageOverlayVolumes = nil
	pv.damageOverlayError = ""

	bones := make(map[stingray.ThinHash]int, len(pv.rootUnitInfo.Bones))
	for i, bone := range pv.rootUnitInfo.Bones {
		if bone.NameHash.Value != 0 {
			bones[bone.NameHash] = i
		}
	}

	physicsID := stingray.NewFileID(pv.rootHash.Name, stingray.Sum("physics"))
	physicsData, exists, err := pv.getResource(physicsID, stingray.DataMain)
	if err != nil {
		pv.damageOverlayError = fmt.Sprintf("Could not read Havok hitboxes: %v", err)
	} else if !exists {
		pv.damageOverlayError = "This unit has no Havok hitbox resource."
	} else {
		actors, err := physics.LoadActorBounds(physicsData)
		if err != nil {
			pv.damageOverlayError = fmt.Sprintf("Could not decode Havok hitboxes: %v", err)
		} else {
			for _, actor := range actors {
				boneIndex, ok := bones[actor.Actor]
				if !ok {
					continue
				}
				// Some units include a root `boss` collision actor whose Havok
				// AABB is the union of the complete creature. It obscures the
				// actual per-part hitboxes, so omit only this model-wide aggregate;
				// smaller authored boss/body actors remain visible.
				if pv.lookupThinHash(actor.Actor) == "boss" && maxBoundsExtent(actor.Min, actor.Max) >= 4.5 {
					continue
				}
				pv.damageOverlayBoxes = append(pv.damageOverlayBoxes, damageOverlayBox{
					actor: actor.Actor,
					bone:  boneIndex,
					min:   mgl32.Vec3(actor.Min),
					max:   mgl32.Vec3(actor.Max),
				})
			}
		}
	}

	index, err := getMountEntityIndex()
	if err != nil {
		return fmt.Errorf("reading authored damage-volume data: %w", err)
	}
	zoneRecords := index.damageDealerZonesForUnit(pv.rootHash.Name)
	unmatched := 0
	for _, zone := range zoneRecords {
		boneIndex, ok := bones[zone.Bone]
		if !ok {
			unmatched++
			continue
		}
		size := mgl32.Vec3(zone.Size)
		if !finitePositiveVec3(size) {
			continue
		}
		name := pv.lookupThinHash(zone.ZoneName)
		if name == "" {
			name = zone.ZoneName.String()
		}
		pv.damageOverlayVolumes = append(pv.damageOverlayVolumes, damageOverlayVolume{
			name:     name,
			shape:    zone.Shape,
			bone:     boneIndex,
			size:     size,
			position: mgl32.Vec3(zone.LocalPosition),
			rotation: mgl32.Vec3(zone.LocalRotationDegrees),
			mode:     zone.Mode,
			damage:   zone.DamageValues,
		})
	}
	if len(pv.damageOverlayVolumes) == 0 {
		if unmatched > 0 {
			pv.damageOverlayError = fmt.Sprintf("Found %d authored damage regions, but none of their bone names matched this unit skeleton.", unmatched)
		} else {
			pv.damageOverlayError = "No authored OverlapDamageComponent regions are linked to this unit in the current build."
		}
	} else if unmatched > 0 {
		pv.damageOverlayError = fmt.Sprintf("Skipped %d authored damage regions whose bones are absent from this unit skeleton.", unmatched)
	}
	if len(pv.damageOverlayBoxes) == 0 && pv.damageOverlayError == "" {
		pv.damageOverlayError = "No Havok actor names matched the unit skeleton; blue hitboxes are unavailable."
	}
	pv.damageOverlayUnit = pv.rootHash
	pv.damageOverlayLoaded = true
	return nil
}

func maxBoundsExtent(minimum, maximum [3]float32) float32 {
	return max(maximum[0]-minimum[0], maximum[1]-minimum[1], maximum[2]-minimum[2])
}

func finitePositiveVec3(v mgl32.Vec3) bool {
	for _, value := range v {
		if value <= 0 || math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
	}
	return true
}

func (pv *UnitPreviewState) ensureDamageBoxRenderer() bool {
	if pv.damageBoxRenderReady {
		return true
	}
	cubeVertices := [8][3]float32{
		{-1, -1, -1}, {-1, -1, 1}, {-1, 1, -1}, {-1, 1, 1},
		{1, -1, -1}, {1, -1, 1}, {1, 1, -1}, {1, 1, 1},
	}
	pv.damageBoxRenderer = createDamageOverlayMesh(cubeVertices[:], aabbIndices[:])
	sphereVertices, sphereIndices := makeDamageOverlaySphere(12, 20)
	pv.damageSphereRenderer = createDamageOverlayMesh(sphereVertices, sphereIndices)
	capsuleVertices, capsuleIndices := makeDamageOverlayCapsule(8, 20)
	pv.damageCapsuleRenderer = createDamageOverlayMesh(capsuleVertices, capsuleIndices)
	pv.damageBoxRenderReady = true
	return true
}

func createDamageOverlayMesh(vertices [][3]float32, indices []uint32) unitPreviewObject {
	obj := unitPreviewObject{}
	obj.genObjects(false, 1)
	gl.BindVertexArray(obj.vao)
	gl.BindBuffer(gl.ARRAY_BUFFER, obj.vbo)
	gl.BufferData(gl.ARRAY_BUFFER, len(vertices)*3*4, gl.Ptr(vertices), gl.STATIC_DRAW)
	gl.VertexAttribPointer(0, 3, gl.FLOAT, false, 3*4, nil)
	gl.EnableVertexAttribArray(0)
	gl.BindBuffer(gl.ELEMENT_ARRAY_BUFFER, obj.ibos[0])
	gl.BufferData(gl.ELEMENT_ARRAY_BUFFER, len(indices)*4, gl.Ptr(indices), gl.STATIC_DRAW)
	gl.BindBuffer(gl.ARRAY_BUFFER, obj.imbo)
	for column := uint32(0); column < 4; column++ {
		location := uint32(7) + column
		gl.EnableVertexAttribArray(location)
		gl.VertexAttribPointerWithOffset(location, 4, gl.FLOAT, false, 7*16, uintptr(column*16))
		gl.VertexAttribDivisor(location, 1)
	}
	for column := uint32(0); column < 3; column++ {
		location := uint32(11) + column
		gl.EnableVertexAttribArray(location)
		gl.VertexAttribPointerWithOffset(location, 3, gl.FLOAT, false, 7*16, uintptr((4+column)*16))
		gl.VertexAttribDivisor(location, 1)
	}
	gl.BindBuffer(gl.ARRAY_BUFFER, 0)
	gl.BindVertexArray(0)
	obj.numVertices = int32(len(vertices))
	obj.numIndices[0] = int32(len(indices))
	obj.numInstances = -1
	return obj
}

func makeDamageOverlaySphere(stacks, slices int) ([][3]float32, []uint32) {
	vertices := make([][3]float32, 0, (stacks+1)*(slices+1))
	for stack := 0; stack <= stacks; stack++ {
		phi := float64(stack) * math.Pi / float64(stacks)
		y := float32(math.Cos(phi))
		ringRadius := float32(math.Sin(phi))
		for slice := 0; slice <= slices; slice++ {
			theta := 2 * math.Pi * float64(slice) / float64(slices)
			vertices = append(vertices, [3]float32{ringRadius * float32(math.Cos(theta)), y, ringRadius * float32(math.Sin(theta))})
		}
	}
	indices := make([]uint32, 0, stacks*slices*6)
	for stack := 0; stack < stacks; stack++ {
		for slice := 0; slice < slices; slice++ {
			first := uint32(stack*(slices+1) + slice)
			second := first + uint32(slices+1)
			indices = append(indices, first, second, first+1, second, second+1, first+1)
		}
	}
	return vertices, indices
}

func makeDamageOverlayCapsule(hemisphereSteps, slices int) ([][3]float32, []uint32) {
	vertices := make([][3]float32, 0, (2*hemisphereSteps+1)*(slices+1))
	appendRing := func(radius, y float32) {
		for slice := 0; slice <= slices; slice++ {
			theta := 2 * math.Pi * float64(slice) / float64(slices)
			vertices = append(vertices, [3]float32{radius * float32(math.Cos(theta)), y, radius * float32(math.Sin(theta))})
		}
	}
	for step := 0; step <= hemisphereSteps; step++ {
		theta := (math.Pi / 2) * float64(step) / float64(hemisphereSteps)
		appendRing(float32(0.5*math.Sin(theta)), float32(0.5+0.5*math.Cos(theta)))
	}
	appendRing(0.5, -0.5)
	for step := 1; step <= hemisphereSteps; step++ {
		theta := (math.Pi / 2) * float64(step) / float64(hemisphereSteps)
		appendRing(float32(0.5*math.Cos(theta)), float32(-0.5-0.5*math.Sin(theta)))
	}
	indices := make([]uint32, 0, (len(vertices)/(slices+1)-1)*slices*6)
	ringCount := len(vertices) / (slices + 1)
	for ring := 0; ring < ringCount-1; ring++ {
		for slice := 0; slice < slices; slice++ {
			first := uint32(ring*(slices+1) + slice)
			second := first + uint32(slices+1)
			indices = append(indices, first, second, first+1, second, second+1, first+1)
		}
	}
	return vertices, indices
}

func (pv *UnitPreviewState) damageOverlayBoneMatrix(bone int) mgl32.Mat4 {
	if bone < 0 || pv.rootUnitInfo == nil || bone >= len(pv.rootUnitInfo.Bones) {
		return mgl32.Ident4()
	}
	if pv.animationEnabled && bone < len(pv.animationGlobals) {
		return pv.animationGlobals[bone]
	}
	return pv.rootUnitInfo.Bones[bone].Matrix
}

func damageOverlayBoxCenterScale(box damageOverlayBox) (mgl32.Vec3, mgl32.Vec3) {
	center := box.min.Add(box.max).Mul(0.5)
	halfSize := box.max.Sub(box.min).Mul(0.5)
	return center, halfSize
}

func (pv *UnitPreviewState) damageOverlayBoxInstance(box damageOverlayBox) mgl32.Mat4 {
	center, halfSize := damageOverlayBoxCenterScale(box)
	return pv.damageOverlayBoneMatrix(box.bone).
		Mul4(mgl32.Translate3D(center[0], center[1], center[2])).
		Mul4(mgl32.Scale3D(halfSize[0], halfSize[1], halfSize[2]))
}

func (pv *UnitPreviewState) damageOverlayVolumeInstance(volume damageOverlayVolume) mgl32.Mat4 {
	// OverlapDamageComponent stores Stingray Euler XYZ angles in degrees.
	// AnglesToQuat's XYZ order matches Stingray's from_euler_angles_xyz;
	// composing Z*Y*X here produced the opposite convention and visibly tilted
	// long bone-local damage boxes away from their authored orientation.
	toRadians := float32(math.Pi / 180)
	rotation := mgl32.AnglesToQuat(
		volume.rotation[0]*toRadians,
		volume.rotation[1]*toRadians,
		volume.rotation[2]*toRadians,
		mgl32.XYZ,
	).Mat4()
	scale := mgl32.Vec3{volume.size[0] * 0.5, volume.size[1] * 0.5, volume.size[2] * 0.5}
	if volume.shape == datalibrary.DamageDealerZoneShapeCapsule {
		// The canonical capsule has a unit diameter and a total height of two.
		scale = mgl32.Vec3{volume.size[0], volume.size[1] * 0.5, volume.size[2]}
	}
	return pv.damageOverlayBoneMatrix(volume.bone).
		Mul4(mgl32.Translate3D(volume.position[0], volume.position[1], volume.position[2])).
		Mul4(rotation).
		Mul4(mgl32.Scale3D(scale[0], scale[1], scale[2]))
}

func appendDamageOverlayInstance(dst []float32, matrix mgl32.Mat4) []float32 {
	dst = append(dst, matrix[:]...)
	for range 12 {
		dst = append(dst, 0)
	}
	return dst
}

func (pv *UnitPreviewState) drawDamageOverlayPass(obj unitPreviewObject, matrices []float32, model, view, projection mgl32.Mat4, color mgl32.Vec4) {
	if len(matrices) == 0 {
		return
	}
	if err := pv.boundingBoxMaterial.SetUniform("color", 1, color); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
		panic(err)
	}
	if err := pv.boundingBoxMaterial.SetUniform("model", 1, model); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
		panic(err)
	}
	if err := pv.boundingBoxMaterial.SetUniform("view", 1, view); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
		panic(err)
	}
	if err := pv.boundingBoxMaterial.SetUniform("projection", 1, projection); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
		panic(err)
	}
	gl.BindVertexArray(obj.vao)
	gl.BindBuffer(gl.ARRAY_BUFFER, obj.imbo)
	gl.BufferData(gl.ARRAY_BUFFER, len(matrices)*4, gl.Ptr(matrices), gl.DYNAMIC_DRAW)
	gl.DrawElementsInstanced(gl.TRIANGLES, obj.numIndices[0], gl.UNSIGNED_INT, nil, int32(len(matrices)/(7*4)))
	gl.BindVertexArray(0)
}

func (pv *UnitPreviewState) drawDamageOverlayBoxes(model, view, projection mgl32.Mat4) {
	if !pv.ensureDamageBoxRenderer() || !pv.boundingBoxMaterial.Use() {
		return
	}
	blendWasEnabled := gl.IsEnabled(gl.BLEND)
	depthWasEnabled := gl.IsEnabled(gl.DEPTH_TEST)
	cullWasEnabled := gl.IsEnabled(gl.CULL_FACE)
	var depthMask int32
	gl.GetIntegerv(gl.DEPTH_WRITEMASK, &depthMask)
	gl.Enable(gl.BLEND)
	gl.BlendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA)
	gl.Disable(gl.DEPTH_TEST)
	gl.Disable(gl.CULL_FACE)
	gl.DepthMask(false)
	if pv.showPhysicsHitboxes {
		pv.damageBoxScratch = pv.damageBoxScratch[:0]
		for _, box := range pv.damageOverlayBoxes {
			pv.damageBoxScratch = appendDamageOverlayInstance(pv.damageBoxScratch, pv.damageOverlayBoxInstance(box))
		}
		pv.drawDamageOverlayPass(pv.damageBoxRenderer, pv.damageBoxScratch, model, view, projection, mgl32.Vec4{0.05, 0.32, 1.0, 0.90})
	}
	if pv.showDamageBoxes {
		for _, group := range []struct {
			shape    datalibrary.DamageDealerZoneShape
			renderer unitPreviewObject
		}{
			{datalibrary.DamageDealerZoneShapeSphere, pv.damageSphereRenderer},
			{datalibrary.DamageDealerZoneShapeBox, pv.damageBoxRenderer},
			{datalibrary.DamageDealerZoneShapeCapsule, pv.damageCapsuleRenderer},
		} {
			pv.damageBoxScratch = pv.damageBoxScratch[:0]
			for _, volume := range pv.damageOverlayVolumes {
				if volume.shape == group.shape {
					pv.damageBoxScratch = appendDamageOverlayInstance(pv.damageBoxScratch, pv.damageOverlayVolumeInstance(volume))
				}
			}
			pv.boundingBoxMaterial.Use()
			pv.drawDamageOverlayPass(group.renderer, pv.damageBoxScratch, model, view, projection, mgl32.Vec4{1.0, 0.04, 0.04, 0.60})
		}
	}
	if depthMask != 0 {
		gl.DepthMask(true)
	} else {
		gl.DepthMask(false)
	}
	if depthWasEnabled {
		gl.Enable(gl.DEPTH_TEST)
	} else {
		gl.Disable(gl.DEPTH_TEST)
	}
	if cullWasEnabled {
		gl.Enable(gl.CULL_FACE)
	} else {
		gl.Disable(gl.CULL_FACE)
	}
	if !blendWasEnabled {
		gl.Disable(gl.BLEND)
	}
	gl.BindVertexArray(0)
	gl.UseProgram(0)
}

func (pv *UnitPreviewState) drawDamageOverlayReadouts(dl *imgui.DrawList, pos, size imgui.Vec2) {
	if pv.rootUnitInfo == nil || len(pv.damageOverlayVolumes) == 0 {
		return
	}
	modelPos, _, view, projection := pv.computeMVP(size.X/size.Y, false)
	sceneModel := pv.previewBaseSceneModelMatrix(modelPos)
	mvp := projection.Mul4(view).Mul4(sceneModel)
	occupied := make(map[[2]int]int)
	fontSize := max(9.0, imgui.FontSize()*0.72)
	white := imgui.ColorU32Vec4(imgui.NewVec4(1, 1, 1, 1))
	backdrop := imgui.ColorU32Vec4(imgui.NewVec4(0, 0, 0, 0.68))
	for _, volume := range pv.damageOverlayVolumes {
		drawDamageOverlayVolumeLabel(pv, dl, pos, size, mvp, volume, occupied, fontSize, white, backdrop)
	}
}

func drawDamageOverlayVolumeLabel(pv *UnitPreviewState, dl *imgui.DrawList, pos, size imgui.Vec2, mvp mgl32.Mat4, volume damageOverlayVolume, occupied map[[2]int]int, fontSize float32, white, backdrop uint32) {
	world := pv.damageOverlayBoneMatrix(volume.bone).Mul4x1(volume.position.Vec4(1)).Vec3()
	clip := mvp.Mul4x1(world.Vec4(1))
	if clip.W() <= 0 || math.IsNaN(float64(clip.W())) || math.IsInf(float64(clip.W()), 0) {
		return
	}
	ndc := clip.Vec3().Mul(1 / clip.W())
	if ndc[2] < -1 || ndc[2] > 1 {
		return
	}
	screen := imgui.NewVec2(
		pos.X+(ndc[0]+1)*0.5*size.X,
		pos.Y+(1-ndc[1])*0.5*size.Y,
	)
	if screen.X < pos.X || screen.X >= pos.X+size.X || screen.Y < pos.Y || screen.Y >= pos.Y+size.Y {
		return
	}
	cell := [2]int{int((screen.X - pos.X) / 18), int((screen.Y - pos.Y) / 18)}
	shape := damageOverlayShapeLabel(volume.shape)
	text := fmt.Sprintf("%s · %s · %.2f×%.2f×%.2f", volume.name, shape, volume.size[0], volume.size[1], volume.size[2])
	if volume.mode == datalibrary.OverlapDamageModeImpactDamage {
		text += fmt.Sprintf(" · impact values %s / %s / %s / %s / %s",
			formatDamageOverlayNumber(volume.damage[0]),
			formatDamageOverlayNumber(volume.damage[1]),
			formatDamageOverlayNumber(volume.damage[2]),
			formatDamageOverlayNumber(volume.damage[3]),
			formatDamageOverlayNumber(volume.damage[4]),
		)
	}
	textSize := imgui.CurrentFont().CalcTextSizeA(fontSize, math.MaxFloat32, 0, text)
	stack := occupied[cell]
	occupied[cell] = stack + 1
	textPos := screen.Add(imgui.NewVec2(7, float32(stack)*fontSize))
	dl.AddRectFilled(textPos.Sub(imgui.NewVec2(2, 1)), textPos.Add(imgui.NewVec2(textSize.X+2, fontSize+1)), backdrop)
	dl.AddTextFontPtr(imgui.CurrentFont(), fontSize, textPos, white, text)
}

func formatDamageOverlayNumber(value float32) string {
	return strconv.FormatFloat(float64(value), 'f', -1, 32)
}

func damageOverlayShapeLabel(shape datalibrary.DamageDealerZoneShape) string {
	switch shape {
	case datalibrary.DamageDealerZoneShapeSphere:
		return "sphere"
	case datalibrary.DamageDealerZoneShapeBox:
		return "box"
	case datalibrary.DamageDealerZoneShapeCapsule:
		return "capsule"
	default:
		return "unknown shape"
	}
}
