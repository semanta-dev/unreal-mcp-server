"""A small, stateful fake of the `unreal` API surface the v2 ops use — enough to run
the op BODIES (not just the resolvers) in CPython. Anything not modelled here raises
AttributeError, so an op that starts using an API the fake (and, often, UE 5.7's
Python) does not have fails loudly instead of passing against a permissive stub."""
import contextlib


class Vector:
    def __init__(self, x=0.0, y=0.0, z=0.0):
        self.x, self.y, self.z = float(x), float(y), float(z)


class Rotator:
    def __init__(self, roll=0.0, pitch=0.0, yaw=0.0):
        self.roll, self.pitch, self.yaw = float(roll), float(pitch), float(yaw)


class Object:
    pass


class StructBase:
    """A struct value: fields set by keyword; get/set_editor_property raise on a field the
    struct does not have, like the engine."""

    def __init__(self, **fields):
        self.__dict__["_f"] = dict(fields)

    def get_editor_property(self, k):
        if k not in self._f:
            raise Exception("Failed to find property '%s' for attribute '%s' on '%s'" % (k, k, type(self).__name__))
        return self._f[k]

    def set_editor_property(self, k, v):
        self.get_editor_property(k)
        self._f[k] = v


class Class(Object):
    def __init__(self, name, path, parent=None):
        self._name, self._path, self.parent = name, path, parent

    def get_name(self):
        return self._name

    def get_path_name(self):
        return self._path
    # Deliberately NO is_child_of: UClass.IsChildOf is not reflected to Python in 5.7.


class Blueprint(Object):
    def __init__(self, cls):
        self._cls = cls

    def generated_class(self):
        return self._cls


class WidgetBlueprint(Blueprint):
    pass


class ActorComponent(Object):
    pass


class ComponentMobility:
    STATIC, STATIONARY, MOVABLE = "Static", "Stationary", "Movable"


class SceneComponent(ActorComponent):
    def __init__(self, mobility=ComponentMobility.MOVABLE):
        self.mobility = mobility

    def get_editor_property(self, k):
        if k != "mobility":
            raise Exception("no property %s" % k)
        return self.mobility

    def set_mobility(self, m):
        self.mobility = m


class StaticMeshComponent(ActorComponent):
    def __init__(self):
        self.mesh = None

    def get_name(self):
        return "StaticMeshComponent0"

    def get_class(self):
        return Class("StaticMeshComponent", "/Script/Engine.StaticMeshComponent")

    def set_static_mesh(self, mesh):
        self.mesh = mesh


class Actor(Object):
    def __init__(self, cls, label, path, functions=None):
        self._cls, self._label, self._path = cls, label, path
        self.loc, self.rot, self.scale = Vector(), Rotator(), Vector(1, 1, 1)
        self.props, self.readonly = {}, set()
        self.tags, self.modified, self.destroyed = [], 0, False
        self.functions = functions or {}
        self.comp = StaticMeshComponent() if cls.get_name() == "StaticMeshActor" else None
        self.root = SceneComponent()
        self.game = False  # a PIE copy: a Static root ignores moves, like the engine

    def get_actor_label(self):
        return self._label

    def set_actor_label(self, v):
        self._label = v

    def get_path_name(self):
        return self._path

    def get_name(self):
        return self._path.rsplit(".", 1)[-1]

    def get_class(self):
        return self._cls

    def get_actor_location(self):
        return self.loc

    def get_actor_rotation(self):
        return self.rot

    def get_actor_scale3d(self):
        return self.scale

    def get_actor_bounds(self, only_colliding_components, include_from_child_actors=False):
        return self.loc, Vector(50, 50, 90)

    def set_actor_location(self, v, sweep, teleport):
        if self.game and self.root.mobility == ComponentMobility.STATIC:
            return False
        self.loc = v
        return True

    def set_actor_rotation(self, r, teleport):
        self.rot = r

    def set_actor_scale3d(self, v):
        self.scale = v

    def get_components_by_class(self, cls):
        return [self.comp] if self.comp else []

    def get_component_by_class(self, cls):
        return self.comp

    def get_editor_property(self, k):
        if k == "tags":
            return list(self.tags)
        if k == "root_component":
            return self.root
        if k not in self.props:
            # UE 5.7's message for a name the class does not have.
            raise Exception("Failed to find property '%s' for attribute '%s' on '%s'" % (k, k, self._cls.get_name()))
        return self.props[k]

    def set_editor_property(self, k, v):
        if k == "tags":
            self.tags = [str(t) for t in v]
            return
        if k in self.readonly:
            raise Exception("property %s is read-only" % k)
        self.props[k] = v

    def get_attach_parent_actor(self):
        return getattr(self, "parent", None)

    def modify(self):
        self.modified += 1
        return True

    def destroy_actor(self):
        self.destroyed = True
        return True

    def call_method(self, name, args=(), kwargs=None):
        if name not in self.functions:
            raise Exception("Failed to find function '%s' on '%s'" % (name, self.get_name()))
        return self.functions[name](**(kwargs or {}))


class _Tx:
    log = []
    kept = []          # the undo buffer: titles of transactions UE kept
    drop_next = False  # model UE dropping a transaction that changed nothing

    def __init__(self, label):
        self.label, self.cancelled = label, False

    def __enter__(self):
        _Tx.log.append(("begin", self.label))
        return self

    def cancel(self):
        # ScopedEditorTransaction.cancel: the transaction is not kept as an undo step.
        self.cancelled = True
        _Tx.log.append(("cancel", self.label))

    def __exit__(self, *exc):
        _Tx.log.append(("end", self.label))
        if _Tx.drop_next:
            _Tx.drop_next = False
        elif not self.cancelled:
            _Tx.kept.append(self.label)
        return False


class ActorDesc:
    def __init__(self, path):
        self.path = path

    def get_editor_property(self, k):
        if k != "actor_path":
            raise Exception("no property %s" % k)
        return self.path


class Package:
    def __init__(self, name):
        self.name = name

    def get_name(self):
        return self.name


class AssetData:
    def __init__(self, name, package):
        self.asset_name, self.package_name = name, package

    def get_editor_property(self, k):
        return {"asset_name": self.asset_name, "package_name": self.package_name}[k]

    def is_valid(self):
        return True

    def get_tag_value(self, key):
        return "/Script/Engine.Actor" if key == "ParentClass" else None


class _ARFilter:
    """5.7: the fields are settable only through the constructor."""

    def __init__(self, **kw):
        self.kw = kw

    def set_editor_property(self, k, v):
        raise Exception("ARFilter: Property '%s' cannot be edited on instances" % k)


class Fake:
    """The `unreal` module. Build one per test; mutate its state directly."""

    def __init__(self):
        self.Vector, self.Rotator, self.Object, self.Class = Vector, Rotator, Object, Class
        self.Actor, self.Blueprint, self.WidgetBlueprint = Actor, Blueprint, WidgetBlueprint
        self.ActorComponent, self.StaticMeshComponent = ActorComponent, StaticMeshComponent
        self.SceneComponent, self.ComponentMobility = SceneComponent, ComponentMobility
        self.StructBase = StructBase
        self.ScopedEditorTransaction = _Tx
        _Tx.log = []
        _Tx.kept = []
        _Tx.drop_next = False
        self.tx = _Tx.log
        self.EditorActorSubsystem, self.UnrealEditorSubsystem = "EditorActorSubsystem", "UnrealEditorSubsystem"
        self.LevelEditorSubsystem = "LevelEditorSubsystem"
        self.classes = {}       # /Script path -> Class
        self.assets = {}        # asset path -> object
        self.deleted = []
        self.editor_actors, self.pie_actors = [], None
        self.selected = []
        self.camera = (Vector(), Rotator())
        self.pawn = self.gamestate = None
        self.project = "MyGame"
        self.seq = 0
        self.saved_dir = "../../../Proj/Saved/"  # FPaths style: relative to Engine/Binaries
        self.dirty_maps = set()
        self.saves = 0
        self.wp_descs = None  # list of actor paths when the level is World Partition
        self.pie_requests = []
        self.Name = str
        self.Paths = _NS(project_saved_dir=lambda: self.saved_dir,
                         convert_relative_path_to_full=lambda p: "/abs/" + p.replace("../", ""))
        self.EditorLoadingAndSavingUtils = _NS(
            save_dirty_packages=self._save,
            get_dirty_map_packages=lambda: [Package(n) for n in sorted(self.dirty_maps)])
        actor = self.add_class("Actor", "/Script/Engine.Actor")
        self.StaticMeshActor = self.add_class("StaticMeshActor", "/Script/Engine.StaticMeshActor", actor)
        for n in ("DirectionalLight", "SkyLight", "SkyAtmosphere", "ExponentialHeightFog", "PostProcessVolume"):
            setattr(self, n, self.add_class(n, "/Script/Engine." + n, actor))
        self.MathLibrary = _Math()
        # UE 5.7's SystemLibrary has no get_project_name (the live R1 run): not modelled.
        self.SystemLibrary = _NS()
        self.GameplayStatics = _NS(
            get_player_pawn=lambda world, i: self.pawn if world == "PIE" else None,
            get_game_state=lambda world: self.gamestate if world == "PIE" else None,
            get_all_actors_of_class=lambda world, cls: list(self.pie_actors or []))
        self.EditorAssetLibrary = _NS(
            does_asset_exist=lambda p: p in self.assets,
            delete_asset=self._delete_asset,
            save_asset=lambda p: True,
            load_asset=lambda p: self.assets.get(p))
        self.AssetRegistryHelpers = _NS(get_asset_registry=lambda: _NS(
            get_assets=self._registry_assets,
            get_assets_by_package_name=lambda pkg: [AssetData(pkg.rsplit("/", 1)[-1], pkg)]
            if pkg in self.assets else []))

    def _save(self, maps, content):
        self.saves += 1
        self.dirty_maps.clear()
        return True

    @property
    def WorldPartitionBlueprintLibrary(self):
        if self.wp_descs is None:
            raise AttributeError("WorldPartitionBlueprintLibrary")
        return _NS(get_actor_descs=lambda: [ActorDesc(p) for p in self.wp_descs])

    # --- builders ------------------------------------------------------------------
    def add_class(self, name, path, parent=None):
        c = Class(name, path, parent)
        self.classes[path] = c
        if path.startswith("/Script/"):
            # Like the real module: a loaded native class is exposed by its short name.
            setattr(self, name, type(name, (), {"static_class": staticmethod(lambda: c)}))
        return c

    def add_blueprint(self, path, parent):
        name = path.rsplit("/", 1)[-1]
        bp = Blueprint(Class(name + "_C", path + "." + name + "_C", parent))
        self.assets[path] = bp
        self.assets[path + "." + name] = bp
        return bp

    def add_actor(self, cls_path, label, world="editor", functions=None):
        self.seq += 1
        lvl = "UEDPIE_0_L" if world == "pie" else "L"
        a = Actor(self.classes[cls_path], label, "/Game/Maps/%s.%s:PersistentLevel.%s_%d" % (lvl, lvl, label, self.seq),
                  functions)
        (self.editor_actors if world == "editor" else self.pie_actors).append(a)
        return a

    # --- API -----------------------------------------------------------------------
    def get_editor_subsystem(self, which):
        return _Subsystem(self)

    def load_class(self, outer, path):
        return self.classes.get(path)

    def find_object(self, outer, path):
        return self.classes.get(path)

    def load_asset(self, path):
        return self.assets.get(path)

    def load_object(self, outer, path):
        return self.classes.get(path) or self.assets.get(path)

    def ARFilter(self, **kw):
        return _ARFilter(**kw)

    def TopLevelAssetPath(self, pkg, name):
        return (pkg, name)

    def _registry_assets(self, flt):
        out = []
        for path, obj in self.assets.items():
            if isinstance(obj, Blueprint) and "." not in path:
                pkg, name = path, path.rsplit("/", 1)[-1]
                out.append(AssetData(name, pkg))
        return out

    def _delete_asset(self, path):
        if path in self.assets:
            self.deleted.append(path)
            for k in [k for k in self.assets if k == path or k.startswith(path + ".")]:
                del self.assets[k]
            return True
        return False


class _NS:
    def __init__(self, **kw):
        self.__dict__.update(kw)


class _Math:
    @staticmethod
    def class_is_child_of(test, parent):
        c = test
        while c is not None:
            if c is parent:
                return True
            c = c.parent
        return False


class _Subsystem:
    """EditorActorSubsystem + UnrealEditorSubsystem + LevelEditorSubsystem in one."""

    def __init__(self, ue):
        self.ue = ue

    def get_editor_world(self):
        return "EDITOR"

    def get_game_world(self):
        return "PIE" if self.ue.pie_actors is not None else None

    def get_all_level_actors(self):
        return list(self.ue.editor_actors)

    def spawn_actor_from_class(self, cls, loc, rot):
        a = self.ue.add_actor(cls.get_path_name(), cls.get_name()) if cls.get_path_name() in self.ue.classes else None
        if a is None:
            self.ue.seq += 1
            a = Actor(cls, cls.get_name(), "/Game/Maps/L.L:PersistentLevel.%s_%d" % (cls.get_name(), self.ue.seq))
            self.ue.editor_actors.append(a)
        a.loc, a.rot = loc, rot
        return a

    def destroy_actor(self, a):
        self.ue.editor_actors.remove(a)
        a.destroyed = True
        return True

    def is_in_play_in_editor(self):
        return self.ue.pie_actors is not None

    def editor_request_begin_play(self):
        self.ue.pie_requests.append("play")

    def editor_play_simulate(self):
        self.ue.pie_requests.append("simulate")

    def editor_request_end_play(self):
        self.ue.pie_requests.append("stop")

    def get_selected_level_actors(self):
        return list(self.ue.selected)

    def set_selected_level_actors(self, actors):
        self.ue.selected = list(actors)

    def get_level_viewport_camera_info(self):
        return self.ue.camera

    def set_level_viewport_camera_info(self, loc, rot):
        self.ue.camera = (loc, rot)


@contextlib.contextmanager
def installed(mod, fake):
    """Point a loaded companion module at `fake` for the duration."""
    old = mod.unreal
    mod.unreal = fake
    try:
        yield fake
    finally:
        mod.unreal = old
