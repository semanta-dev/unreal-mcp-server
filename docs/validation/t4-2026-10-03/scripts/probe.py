import unreal
out = []
for bp in unreal.ObjectIterator(unreal.Blueprint):
    if "EasySaveGameOperations" in bp.get_name():
        for prop in ("status", "b_display_compile_pie_warning", "display_compile_pie_warning"):
            try:
                out.append((prop, str(bp.get_editor_property(prop))))
            except Exception as e:
                out.append((prop, "ERR " + str(e)[:80]))
        try:
            out.append(("set_flag", str(bp.set_editor_property("display_compile_pie_warning", False))))
        except Exception as e:
            out.append(("set_flag", "ERR " + str(e)[:100]))
        out.append(("dir", [a for a in dir(bp) if "status" in a.lower() or "compile" in a.lower() or "pie" in a.lower()]))
print(out)
print([n for n in dir(unreal.BlueprintEditorLibrary) if not n.startswith("_")])
